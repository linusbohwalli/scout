package scout

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
	pb "github.com/linusbohwalli/scout/api"
	"github.com/nats-io/gnatsd/server"
	nats "github.com/nats-io/go-nats"
	"github.com/pkg/errors"
	"github.com/rjeczalik/notify"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	configPath = "/etc/scout/scout.yaml"
)

//config defines configurable settings for the scouting
//ScanPath sets path of where to scan for files
//LogPath sets path to write log files
//MaxBuffers sets how big the buffered channels should be
//Exclude is a slice of extensions to be excluded from scouting
//PubHost sets publisher client host
//PubPort sets publisher client port
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

	//Read config file
	file, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config *config

	//Unmarshal yaml to config struct
	if err := yaml.Unmarshal(file, &config); err != nil {
		return nil, errors.New("ERROR: Unable to unmarshal config")
	}

	return config, nil
}

//report contains information about a ScoutReport and FileInfo for concerned file that needs to be published to subscribers
type report struct {
	*pb.ScoutReport
}

//scout defines the structure of scout
//reportChan is initiated to receive events from notify
//newReportChan is initiated to send and receive new events
//errorChan is initiated to send and receive scout errors
//log, config, conn, lock is wrappers
type scout struct {
	reportChan    chan notify.EventInfo
	newReportChan chan *report
	errorChan     chan error
	shutdownChan  chan bool
	log           *log.Logger
	config        *config
	conn          *nats.Conn
	lock          *sync.Mutex
}

//NewScout returns a new scout
func NewScout() (*scout, error) {

	config, err := newConfig()
	if err != nil {
		return nil, err
	}

	//TODO: fix switch statement?
	if config == nil || config.LogPath == "" || config.ScanPath == "" || config.PubHost == "" || config.PubPort == 0 {
		return nil, errors.New("ERROR: Config is empty or missing mandatory data, please make sure to fill out mandatory values in the config file")
	}

	logger := new(log.Logger)
	logger.SetOutput(&lumberjack.Logger{Filename: config.LogPath, MaxSize: 100})

	return &scout{
		reportChan:    make(chan notify.EventInfo, config.MaxBuffers),
		newReportChan: make(chan *report, config.MaxBuffers),
		errorChan:     make(chan error, config.MaxBuffers),
		shutdownChan:  make(chan bool),
		log:           logger,
		config:        config,
		lock:          &sync.Mutex{},
	}, nil
}

//handleNewReport handles the received event from reportChan and notify
func (sct *scout) handleNewReport(ev notify.EventInfo) {

	stat, err := os.Stat(ev.Path())
	if err != nil {
		if err.Error() == "stat "+ev.Path()+": "+"no such file or directory" {
			//TODO: Ignore this error? Better handling?
			return
		}
		sct.errorChan <- err
		return
	}

	if stat.IsDir() {
		//Directory handling not implemented...
		return
	}

	if (ev.Event() == notify.Create || ev.Event() == notify.Rename) && !stat.IsDir() {

		for _, v := range sct.config.Exclude {

			//Do not proceed with excluded files and directories
			if strings.Contains(ev.Path(), v) {
				sct.errorChan <- errors.Errorf("INFO: %v is excluded from report handling", ev.Path())
				return
			}
		}

		report := sct.newReport(ev, stat)
		if report == nil {
			sct.errorChan <- errors.New("ERROR: New report returned NIL")
		}

		sct.newReportChan <- report
	}
}

//newReport receives a notify watcher event, and the FileInfo from Stat command on the particular event
//returns a pointer to an event to be published to any listeners
func (sct *scout) newReport(ev notify.EventInfo, stat os.FileInfo) *report {

	//map report data
	return &report{
		&pb.ScoutReport{
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

//Start starts the scout by watching the ScanPath which is defined in /etc/scout/scout.yaml file.
//The scout is able to recursively watch a directory. Example to watch current directory recursively: "./..."
func (sct *scout) Start() {

	if err := notify.Watch(sct.config.ScanPath, sct.reportChan, notify.Create, notify.Rename); err != nil {
		sct.log.Printf("ERROR: %v", errors.New("Unable to recursively watch current working directory"))
	}

	//Start Embedded NATS Server
	s := runDefaultServer()
	defer s.Shutdown()

	//Open publisher client connection
	addr := fmt.Sprintf("nats://%v:%d", sct.config.PubHost, sct.config.PubPort)
	conn, err := nats.Connect(addr)
	if err != nil {
		sct.log.Printf("ERROR: Error during connection to NATS server: %v", err)
	}

	//assign conn to scout
	sct.conn = conn
	defer sct.conn.Close()

	//init go routine for event and error
	go func() {
		for {
			select {
			case ev := <-sct.reportChan:
				//spin up go routines to handleNewReport
				go sct.handleNewReport(ev)
			case newReport := <-sct.newReportChan:
				sct.lock.Lock()

				data := fmt.Sprintf("%v", newReport.ScoutReport)
				msg := &nats.Msg{Subject: "ScoutReport", Data: []byte(data)}
				sct.conn.PublishMsg(msg)

				sct.log.Printf("INFO: Published msg to NATS server: Subject: %v | %v", msg.Subject, string(msg.Data))

				sct.lock.Unlock()

			case err := <-sct.errorChan:
				sct.log.Printf("ERROR: %v", err)
			}
		}
	}()

	//Blocking receive, which will indicate when we release go routine and shutdown scout
	<-sct.shutdownChan
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
