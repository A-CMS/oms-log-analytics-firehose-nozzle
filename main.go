package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/signal"
	"runtime/pprof"
	"strings"
	"syscall"
	"time"

	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/caching"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/client"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/ehclient"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/ehnozzle"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/firehose"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/omsnozzle"
	cfclient "github.com/cloudfoundry-community/go-cfclient"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

const (
	firehoseSubscriptionID = "oms-nozzle"
	// lower limit for override
	minOMSPostTimeoutSeconds = 1
	// upper limit for override
	maxOMSPostTimeoutSeconds = 60
	// upper limit of max message number per batch
	ceilingMaxMsgNumPerBatch = 10000
	// filter metrics
	metricEventType = "METRIC"
	// filter stdout/stderr events
	logEventType = "LOG"
	// filter http start/stop events
	httpEventType = "HTTP"
	// the prefix of message type in OMS Log Analytics
	omsTypePrefix = "CF_"

	version = "1.1.2"
)

// Required parameters
var (
	apiAddress     = kingpin.Flag("api-addr", "Api URL").OverrideDefaultFromEnvar("API_ADDR").String()
	dopplerAddress = kingpin.Flag("doppler-addr", "Traffic controller URL").OverrideDefaultFromEnvar("DOPPLER_ADDR").String()
	cfUser         = kingpin.Flag("firehose-user", "CF user with admin and firehose access").OverrideDefaultFromEnvar("FIREHOSE_USER").Required().String()
	cfPassword     = kingpin.Flag("firehose-user-password", "Password of the CF user").OverrideDefaultFromEnvar("FIREHOSE_USER_PASSWORD").Required().String()
	environment    = kingpin.Flag("cf-environment", "CF environment name").OverrideDefaultFromEnvar("CF_ENVIRONMENT").Default("cf").String()
	sink           = kingpin.Flag("sink", "Sink implementation to use").OverrideDefaultFromEnvar("SINK").Default("oms").String()

	// OMS
	omsWorkspace         = kingpin.Flag("oms-workspace", "OMS workspace ID").OverrideDefaultFromEnvar("OMS_WORKSPACE").String()
	omsKey               = kingpin.Flag("oms-key", "OMS workspace key").OverrideDefaultFromEnvar("OMS_KEY").String()
	omsPostTimeout       = kingpin.Flag("oms-post-timeout", "HTTP timeout for posting events to OMS Log Analytics").Default("5s").OverrideDefaultFromEnvar("OMS_POST_TIMEOUT").Duration()
	omsBatchTime         = kingpin.Flag("oms-batch-time", "Interval to post an OMS batch").Default("5s").OverrideDefaultFromEnvar("OMS_BATCH_TIME").Duration()
	omsMaxMsgNumPerBatch = kingpin.Flag("oms-max-msg-num-per-batch", "Max number of messages per OMS batch").Default("1000").OverrideDefaultFromEnvar("OMS_MAX_MSG_NUM_PER_BATCH").Int()

	// Event Hubs
	ehConnectionString  = kingpin.Flag("eh-connection-string", "EH wconnection string").OverrideDefaultFromEnvar("EVENTHUB_CONNECTION_STRING").String()
	ehPostTimeout       = kingpin.Flag("eh-post-timeout", "HTTP timeout for posting events to EH").Default("5s").OverrideDefaultFromEnvar("EVENTHUB_POST_TIMEOUT").Duration()
	ehBatchTime         = kingpin.Flag("eh-batch-time", "Interval to post an EH batch").Default("3s").OverrideDefaultFromEnvar("EVENTHUB_BATCH_TIME").Duration()
	ehMaxMsgNumPerBatch = kingpin.Flag("eh-max-msg-num-per-batch", "Max number of messages per EH batch").Default("200").OverrideDefaultFromEnvar("EVENTHUB_MAX_MSG_NUM_PER_BATCH").Int()

	// comma separated list of types to exclude.  For now use metric,log,http and revisit later
	spaceFilter           = kingpin.Flag("appFilter", "Comma separated white list of app spaces").Default("").OverrideDefaultFromEnvar("SPACE_WHITELIST").String()
	eventFilter           = kingpin.Flag("eventFilter", "Comma separated list of types to exclude").Default("").OverrideDefaultFromEnvar("EVENT_FILTER").String()
	skipSslValidation     = kingpin.Flag("skip-ssl-validation", "Skip SSL validation").Default("false").OverrideDefaultFromEnvar("SKIP_SSL_VALIDATION").Bool()
	idleTimeout           = kingpin.Flag("idle-timeout", "Keep Alive duration for the firehose consumer").Default("25s").OverrideDefaultFromEnvar("IDLE_TIMEOUT").Duration()
	logLevel              = kingpin.Flag("log-level", "Log level: DEBUG, INFO, ERROR").Default("INFO").OverrideDefaultFromEnvar("LOG_LEVEL").String()
	logEventCount         = kingpin.Flag("log-event-count", "Whether to log the total count of received and sent events to OMS").Default("false").OverrideDefaultFromEnvar("LOG_EVENT_COUNT").Bool()
	logEventCountInterval = kingpin.Flag("log-event-count-interval", "The interval to log the total count of received and sent events to OMS").Default("60s").OverrideDefaultFromEnvar("LOG_EVENT_COUNT_INTERVAL").Duration()

	excludeMetricEvents = false
	excludeLogEvents    = false
	excludeHttpEvents   = false
)

func main() {
	kingpin.Version(version)
	kingpin.Parse()

	logger := lager.NewLogger("oms-nozzle")
	level := lager.INFO
	switch strings.ToUpper(*logLevel) {
	case "DEBUG":
		level = lager.DEBUG
	case "ERROR":
		level = lager.ERROR
	}
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, level))

	// enable thread dump
	threadDumpChan := registerGoRoutineDumpSignalChannel()
	defer close(threadDumpChan)
	go dumpGoRoutine(threadDumpChan)

	var ENVIRONMENT_API_ADDR string
	var VCAP_APPLICATION map[string]*json.RawMessage
	err := json.Unmarshal([]byte(os.Getenv("VCAP_APPLICATION")), &VCAP_APPLICATION)
	if err != nil {
		logger.Error("environment variable unmarshal failed", errors.New(err.Error()))
	} else {
		err = json.Unmarshal(*VCAP_APPLICATION["cf_api"], &ENVIRONMENT_API_ADDR)
		if err != nil {
			logger.Error("environment variable unmarshal failed", errors.New(err.Error()))
		}
	}

	if len(*apiAddress) <= 0 {
		apiAddress = &ENVIRONMENT_API_ADDR
	}
	logger.Info("config", lager.Data{"API_ADDR": *apiAddress})

	if len(*dopplerAddress) <= 0 {
		temp := strings.Replace(*apiAddress, "https://api.", "wss://doppler.", 1) + ":443"
		dopplerAddress = &temp
	}
	logger.Info("config", lager.Data{"DOPPLER_ADDR": *dopplerAddress})

	// Client to use
	if *sink == "oms" {
		logger.Info("Using OMS sink implementation")
		// OMS-specific parameters
		if *omsWorkspace == "" || *omsKey == "" {
			logger.Fatal("Missing OMS parameters", errors.New("both oms-workspace and oms-key are required"))
		}
		if maxOMSPostTimeoutSeconds >= omsPostTimeout.Seconds() && minOMSPostTimeoutSeconds <= omsPostTimeout.Seconds() {
			logger.Info("config", lager.Data{"OMS_POST_TIMEOUT": (*omsPostTimeout).String()})
		} else {
			logger.Info("invalid OMS_POST_TIMEOUT value, set to default",
				lager.Data{"invalid value": (*omsPostTimeout).String()},
				lager.Data{"min seconds": minOMSPostTimeoutSeconds},
				lager.Data{"max seconds": maxOMSPostTimeoutSeconds},
				lager.Data{"default seconds": 5})
			*omsPostTimeout = time.Duration(5) * time.Second
		}
		logger.Info("config", lager.Data{"OMS_BATCH_TIME": (*omsBatchTime).String()})
		if ceilingMaxMsgNumPerBatch >= *omsMaxMsgNumPerBatch && *omsMaxMsgNumPerBatch > 0 {
			logger.Info("config", lager.Data{"OMS_MAX_MSG_NUM_PER_BATCH": *omsMaxMsgNumPerBatch})
		} else {
			logger.Info("invalid OMS_MAX_MSG_NUM_PER_BATCH value, set to default",
				lager.Data{"invalid value": *omsMaxMsgNumPerBatch},
				lager.Data{"max value": ceilingMaxMsgNumPerBatch},
				lager.Data{"default value": 1000})
			*omsMaxMsgNumPerBatch = 1000
		}
	} else if *sink == "eventhubs" {
		logger.Info("USing Event Hubs sink implementation")
		// Event Hubs-specific parameters
		if *ehConnectionString == "" {
			logger.Fatal("Missing Event Hubs parameter", errors.New("eh-connection-string is required"))
		}
	} else {
		logger.Fatal("Could not determine sink to use", errors.New("SINK should be set to either 'oms' or 'eventhubs'"))
	}

	logger.Info("config", lager.Data{"SKIP_SSL_VALIDATION": *skipSslValidation})
	logger.Info("config", lager.Data{"IDLE_TIMEOUT": (*idleTimeout).String()})
	logger.Info("config", lager.Data{"CF_ENVIRONMENT": *environment})

	// Misc parameters
	logger.Info("config", lager.Data{"LOG_EVENT_COUNT": *logEventCount})
	logger.Info("config", lager.Data{"LOG_EVENT_COUNT_INTERVAL": (*logEventCountInterval).String()})
	if len(*eventFilter) > 0 {
		*eventFilter = strings.ToUpper(*eventFilter)
		// by default we don't filter any events
		if strings.Contains(*eventFilter, metricEventType) {
			excludeMetricEvents = true
		}
		if strings.Contains(*eventFilter, logEventType) {
			excludeLogEvents = true
		}
		if strings.Contains(*eventFilter, httpEventType) {
			excludeHttpEvents = true
		}
		logger.Info("config", lager.Data{"EVENT_FILTER": *eventFilter},
			lager.Data{"excludeMetricEvents": excludeMetricEvents},
			lager.Data{"excludeLogEvents": excludeLogEvents},
			lager.Data{"excludeHTTPEvents": excludeHttpEvents})
	} else {
		logger.Info("config EVENT_FILTER is nil. all events will be published")
	}

	cfClientConfig := &cfclient.Config{
		ApiAddress:        *apiAddress,
		Username:          *cfUser,
		Password:          *cfPassword,
		SkipSslValidation: *skipSslValidation,
	}

	firehoseConfig := &firehose.FirehoseConfig{
		SubscriptionId:       firehoseSubscriptionID,
		TrafficControllerUrl: *dopplerAddress,
		IdleTimeout:          *idleTimeout,
	}

	firehoseClient := firehose.NewClient(cfClientConfig, firehoseConfig, logger)

	cachingClient := caching.NewCaching(cfClientConfig, logger, *environment, *spaceFilter)

	if *sink == "oms" {
		omsClient := client.NewOmsClient(*omsWorkspace, *omsKey, *omsPostTimeout, logger)
		nozzleConfig := &omsnozzle.NozzleConfig{
			OmsTypePrefix:         omsTypePrefix,
			OmsBatchTime:          *omsBatchTime,
			OmsMaxMsgNumPerBatch:  *omsMaxMsgNumPerBatch,
			ExcludeMetricEvents:   excludeMetricEvents,
			ExcludeLogEvents:      excludeLogEvents,
			ExcludeHttpEvents:     excludeHttpEvents,
			LogEventCount:         *logEventCount,
			LogEventCountInterval: *logEventCountInterval,
		}
		nozzle := omsnozzle.NewOmsNozzle(logger, firehoseClient, omsClient, nozzleConfig, cachingClient)
		nozzle.Start()
	} else if *sink == "eventhubs" {
		ehClient := ehclient.NewEventHubsClient(*ehConnectionString, logger)
		nozzleConfig := &ehnozzle.NozzleConfig{
			EHBatchTime:           *ehBatchTime,
			EHMaxMsgNumPerBatch:   *ehMaxMsgNumPerBatch,
			ExcludeMetricEvents:   excludeMetricEvents,
			ExcludeLogEvents:      excludeLogEvents,
			ExcludeHttpEvents:     excludeHttpEvents,
			LogEventCount:         *logEventCount,
			LogEventCountInterval: *logEventCountInterval,
		}
		nozzle := ehnozzle.NewEventsHubsNozzle(logger, firehoseClient, ehClient, nozzleConfig, cachingClient)
		nozzle.Start()
	}
}

func registerGoRoutineDumpSignalChannel() chan os.Signal {
	threadDumpChan := make(chan os.Signal, 1)
	signal.Notify(threadDumpChan, syscall.SIGUSR1)

	return threadDumpChan
}

func dumpGoRoutine(dumpChan chan os.Signal) {
	for range dumpChan {
		goRoutineProfiles := pprof.Lookup("goroutine")
		if goRoutineProfiles != nil {
			goRoutineProfiles.WriteTo(os.Stdout, 2)
		}
	}
}
