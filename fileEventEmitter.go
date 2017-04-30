package fileEventEmitter

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	yaml "gopkg.in/yaml.v2"

	"github.com/google/uuid"
	pb "github.com/linusbohwalli/fileEventEmitter/api"
	"github.com/nats-io/gnatsd/server"
	nats "github.com/nats-io/go-nats"
	"github.com/pkg/errors"
	"github.com/rjeczalik/notify"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	configPath = "/Users/linus/development/go/src/github.com/linusbohwalli/fileEventEmitter/config.yaml"
)

//config defines configuration files for eventemitting
type config struct {
	ScanPath   string
	LogPath    string
	MaxBuffers int
	Exclude    []string
	PubHost    string
	PubPort    int
}

//newConfig returns a config
func newConfig() (*config, error) {

	//Read config.yaml
	file, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config *config

	//Unmarshal yaml to config struct
	if err := yaml.Unmarshal(file, &config); err != nil {
		return nil, errors.New("Unable to unmarshal config")
	}

	return config, nil
}

//Event contains information about a FileEvent and FileInfo for concerned file that needs to be published to subscribers
type event struct {
	*pb.FileEvent
}

//FileEventEmitter defines the fileEventEmitter structure
//Wraps log.Logger
type fileEventEmitter struct {
	eventChan    chan notify.EventInfo
	newEventChan chan *event
	errorChan    chan error
	shutdownChan chan bool
	log          *log.Logger
	config       *config
	conn         *nats.Conn
	lock         *sync.Mutex
}

//NewFileEventEmitter returns a new fileEventEmitter
//Will read config file from configPath
func NewFileEventEmitter() (*fileEventEmitter, error) {

	config, err := newConfig()
	if err != nil {
		return nil, err
	}

	logger := new(log.Logger)
	logger.SetOutput(&lumberjack.Logger{Filename: config.LogPath, MaxSize: 100})

	return &fileEventEmitter{
		eventChan:    make(chan notify.EventInfo, config.MaxBuffers),
		newEventChan: make(chan *event, config.MaxBuffers),
		errorChan:    make(chan error, config.MaxBuffers),
		shutdownChan: make(chan bool),
		log:          logger,
		config:       config,
		lock:         new(sync.Mutex),
	}, nil
}

//handleNewEvent handles the received event from eventChan and notify
func (fee *fileEventEmitter) handleNewEvent(ev notify.EventInfo) {

	stat, err := os.Stat(ev.Path())
	if err != nil {
		if err.Error() == "stat "+ev.Path()+": "+"no such file or directory" {
			fee.errorChan <- errors.New("file or directory was either removed or did not exist")
			return
		}
		fee.errorChan <- err
		return
	}

	if stat.IsDir() {
		//Directory handling not implemented...
		return
	}

	if (ev.Event() == notify.Create || ev.Event() == notify.Rename) && !stat.IsDir() {

		for _, v := range fee.config.Exclude {
			if strings.HasSuffix(stat.Name(), v) {
				fee.errorChan <- errors.Errorf("File, %v is excluded from event handling", stat.Name())
				return
			}
		}

		//Do not proceed with temp directories
		if strings.Contains(ev.Path(), "temp") {
			fee.errorChan <- errors.Errorf("File, %v is excluded from event handling due to being in a temp directory", stat.Name())
			return
		}

		//TODO: implement error logic for newEvent
		event := fee.newEvent(ev, stat)
		if event == nil {
			fee.errorChan <- errors.New("Something went wrong during creation of newEvent")
		}

		fee.newEventChan <- event
	}
}

//newEvent receives a notify watcher event, and the FileInfo from Stat command on the particular event
//returns a pointer to an event to be published to any listeners
func (fee *fileEventEmitter) newEvent(ev notify.EventInfo, stat os.FileInfo) *event {

	//TODO: implement error logic
	//map event data
	return &event{
		&pb.FileEvent{
			Fetch:     true,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			UUID:      uuid.New().String(),
			FileInfo: &pb.FileInfo{
				Path:     ev.Path(),
				Filename: stat.Name(),
				Size:     stat.Size(),
			},
		},
	}
}

//Start starts the fileeventemitter by watching the ScanPath which is defined in the config.yaml file.
//Recursive watching is supported. Example to watch current directory recursively: "./..."
func (fee *fileEventEmitter) Start() {

	//implement go routine function?
	if err := notify.Watch(fee.config.ScanPath, fee.eventChan, notify.Create, notify.Rename); err != nil {
		fee.log.Printf("%v", errors.New("Unable to recursively watch current working directory"))
	}

	//Start Embedded NATS Server
	s := runDefaultServer()
	defer s.Shutdown()

	addr := fmt.Sprintf("nats://%v:%d", fee.config.PubHost, fee.config.PubPort)
	conn, err := nats.Connect(addr)
	if err != nil {
		fee.log.Printf("Error during connection to NATS server: %v", err)
	}

	fee.conn = conn
	defer fee.conn.Close()

	//init go routine for event and error
	go func() {
		for {
			select {
			case ev := <-fee.eventChan:
				//spin up go routines to handleNewEvent
				go fee.handleNewEvent(ev)
			case newEvent := <-fee.newEventChan:
				fee.lock.Lock()
				fee.log.Printf("Received new event via channel: %v", newEvent)

				data := fmt.Sprintf("%v", newEvent.FileEvent)
				msg := &nats.Msg{Subject: "fileEvent", Data: []byte(data)}
				fee.conn.PublishMsg(msg)

				fee.log.Printf("Published followong msg to NATS server: Subject: %v | %v", msg.Subject, string(msg.Data))

				fee.lock.Unlock()

			case err := <-fee.errorChan:
				fee.log.Printf("Error: %v", err)
			}
		}
	}()

	//Blocking receive, which will indicate when we release go routine and shutdown fileEventEmitter
	<-fee.shutdownChan
}

//Stop stops the service
func (fee *fileEventEmitter) Stop() {

	//close channels
	close(fee.errorChan)
	close(fee.eventChan)
	close(fee.newEventChan)
	fee.shutdownChan <- true
	//TODO: Find solution to fix closing
}

//Embedded NATS server handling
//serverOptions are default options for the NATS server
var serverOptions = server.Options{
	Host: "localhost",
	Port: 4222,
}

// RunDefaultServer starts a new Go routine based server using the default options
func runDefaultServer() *server.Server {
	return runServer(&serverOptions)
}

// RunServer starts a new Go routine based server
func runServer(opts *server.Options) *server.Server {
	if opts == nil {
		opts = &serverOptions
	}
	s := server.New(opts)
	if s == nil {
		panic("No NATS Server object returned.")
	}

	// Run server in Go routine.
	go s.Start()

	// Wait for accept loop(s) to be started
	if !s.ReadyForConnections(10 * time.Second) {
		panic("Unable to start NATS Server in Go Routine")
	}
	return s
}
