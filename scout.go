package scout

/*
scout is a tool using rjecazlik notify lib to listen on system for file events.
TODO: specify what events and why scout reports.
The scout will report any configured file events to an embedded nats server. The reports are published on the topic "ScoutReport".
User can use the reports to determine if their application should take any action on the reported file or not. Users will have to use a nats client to listen to any ScoutReports published on the embedded NATS server.
For more information regarding nats clients and servers, please visit nats.io

The common use case for scout is for server suppliers to implement a push method from their server instead of polling files.

I.e. Server supplier supports an FTP server for user to send files to, instead of checking directories with a polling solution, scout will report back to any listening subscribers if any files have been sent to your server.
The scout support recursive scouting which makes it very simple to check for files on your system. See scout.yaml for examples.

*/


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

var (
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


	//Configuration for Nats server used for publishing
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

	if file == nil {
		log.Fatalf("No config file defined in config path: %v", configPath)
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
//TODO: fix configurable config path
func NewScout(cnf string) (*scout, error) {

	if cnf != "" {
		configPath = cnf
	}

	config, err := newConfig()
	if err != nil {
		return nil, err
	}

	if config == nil || config.LogPath == "" || config.ScanPath == "" {
		return nil, errors.New("Config is empty or missing mandatory data, please make sure to fill out mandatory values in the config file")
	}

	logger := &log.Logger{}
	logger.SetOutput(&lumberjack.Logger{Filename: config.LogPath, MaxSize: 100})
	//Make sure scout got access to writing logs
	f, err := os.OpenFile(config.LogPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}

	if _, err := f.WriteString(""); err != nil {
		fmt.Printf("Unable to write to logfile, please make sure application got permissions to write, panicing...")
		panic(err)
	}

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
func (sct *scout) Start(def bool) {

	if err := notify.Watch(sct.config.ScanPath, sct.reportChan, notify.Create, notify.Rename); err != nil {
		sct.log.Printf("ERROR: %v", errors.New("Unable to recursively watch current working directory"))
	}

	var s *server.Server
	//Start Embedded NATS Server


	switch {

	//if default flag is provided use defualt options
	case def:
		//set default client connection info, to publish events
		sct.config.PubHost = "localhost"
		sct.config.PubPort = 4222

		fmt.Printf("Starting the default Nats server and initiating client to publish reports on: nats://%v:%d\n", sct.config.PubHost, sct.config.PubPort)

		//start Default Nats server
		s = runDefaultServer()

	//Check if user supplied config
	case sct.config.PubHost != "" && sct.config.PubPort != 0:

		fmt.Printf("Starting Nats server and initiating client to publish reports on: nats://%v:%d\n", sct.config.PubHost, sct.config.PubPort)

		s = runServer(&server.Options{
			Host: sct.config.PubHost,
			Port: sct. config.PubPort,
		})

	default:
		panic(errors.New("No server options set, please either configure your server in scout.yaml or use default options by adding default argument: scout start default"))
	}

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


	fmt.Printf("%v\n", "Scout successfully started...")

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

//defaultServerOptions are default options for the NATS server and will be used if user have not defined any options in the config file
//Default server options is setup to enable quick configuration during development and troubleshooting of the application
var defaultServerOptions = server.Options{
	Host: "localhost",
	Port: 4222,
}

//RunDefaultServer starts a new nats server in a go routine using the default options
func runDefaultServer() *server.Server {
	return runServer(&defaultServerOptions)
}

//RunServer starts a new Go routine based server
func runServer(opts *server.Options) *server.Server {
	if opts == nil {
		opts = &defaultServerOptions
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
