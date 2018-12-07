package common

import (
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/caching"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/firehose"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/messages"
	"github.com/cloudfoundry/sonde-go/events"
)

type Message interface{}

type NozzleConfig struct {
	BatchTime             time.Duration
	MaxMsgNumPerBatch     int
	ExcludeMetricEvents   bool
	ExcludeLogEvents      bool
	ExcludeHttpEvents     bool
	LogEventCount         bool
	LogEventCountInterval time.Duration
}

type Nozzle interface {
	Start() error
}

type NozzleBase struct {
	Client              Client
	Logger              lager.Logger
	TotalEventsReceived uint64
	TotalEventsSent     uint64
	TotalEventsLost     uint64
	TotalDataSent       uint64
	Mutex               *sync.Mutex
	NozzleConfig        *NozzleConfig
	GoroutineSem        chan int // to control the number of active post goroutines

	errChan        <-chan error
	msgChan        <-chan *events.Envelope
	signalChan     chan os.Signal
	firehoseClient firehose.Client
	cachingClient  caching.CachingClient

	PostData func(events *map[string][]interface{}, addCount bool)
}

func NewNozzle(logger lager.Logger, firehoseClient firehose.Client, client Client, nozzleConfig *NozzleConfig, caching caching.CachingClient) *NozzleBase {
	maxPostGoroutines := int(100000 / nozzleConfig.MaxMsgNumPerBatch)
	return &NozzleBase{
		Client:              client,
		Logger:              logger,
		TotalEventsReceived: uint64(0),
		TotalEventsSent:     uint64(0),
		TotalEventsLost:     uint64(0),
		TotalDataSent:       uint64(0),
		Mutex:               &sync.Mutex{},
		NozzleConfig:        nozzleConfig,
		GoroutineSem:        make(chan int, maxPostGoroutines),

		errChan:        make(<-chan error),
		msgChan:        make(<-chan *events.Envelope),
		signalChan:     make(chan os.Signal, 2),
		firehoseClient: firehoseClient,
		cachingClient:  caching,
	}
}

// Start the nozzle.
func (nozzle *NozzleBase) Start() error {
	nozzle.cachingClient.Initialize(true)

	// setup for termination signal from CF
	signal.Notify(nozzle.signalChan, syscall.SIGTERM, syscall.SIGINT)

	nozzle.msgChan, nozzle.errChan = nozzle.firehoseClient.Connect()

	if nozzle.NozzleConfig.LogEventCount {
		nozzle.logTotalEvents(nozzle.NozzleConfig.LogEventCountInterval)
	}
	err := nozzle.routeEvents()
	return err
}

func (nozzle *NozzleBase) logTotalEvents(interval time.Duration) {
	logEventCountTicker := time.NewTicker(interval)
	lastReceivedCount := uint64(0)
	lastSentCount := uint64(0)
	lastLostCount := uint64(0)

	go func() {
		for range logEventCountTicker.C {
			timeStamp := time.Now().UnixNano()
			totalReceivedCount := nozzle.TotalEventsReceived
			totalSentCount := nozzle.TotalEventsSent
			totalLostCount := nozzle.TotalEventsLost
			currentEvents := make(map[string][]interface{})

			// Generate CounterEvent
			nozzle.addEventCountEvent("eventsReceived", totalReceivedCount-lastReceivedCount, totalReceivedCount, &timeStamp, &currentEvents)
			nozzle.addEventCountEvent("eventsSent", totalSentCount-lastSentCount, totalSentCount, &timeStamp, &currentEvents)
			nozzle.addEventCountEvent("eventsLost", totalLostCount-lastLostCount, totalLostCount, &timeStamp, &currentEvents)

			nozzle.GoroutineSem <- 1
			nozzle.PostData(&currentEvents, false)

			lastReceivedCount = totalReceivedCount
			lastSentCount = totalSentCount
			lastLostCount = totalLostCount
		}
	}()
}

func (nozzle *NozzleBase) addEventCountEvent(name string, deltaCount uint64, count uint64, timeStamp *int64, currentEvents *map[string][]interface{}) {
	counterEvent := &events.CounterEvent{
		Name:  &name,
		Delta: &deltaCount,
		Total: &count,
	}

	eventType := events.Envelope_CounterEvent
	job := "nozzle"
	origin := "stats"
	envelope := &events.Envelope{
		EventType:    &eventType,
		Timestamp:    timeStamp,
		Job:          &job,
		Origin:       &origin,
		CounterEvent: counterEvent,
	}

	var msg Message
	eventTypeString := eventType.String()
	msg = messages.NewCounterEvent(envelope, nozzle.cachingClient)
	(*currentEvents)[eventTypeString] = append((*currentEvents)[eventTypeString], msg)
}

func (nozzle *NozzleBase) routeEvents() error {
	pendingEvents := make(map[string][]interface{})
	// Firehose message processing loop
	ticker := time.NewTicker(nozzle.NozzleConfig.BatchTime)
	for {
		// loop over message and signal channel
		select {
		case s := <-nozzle.signalChan:
			nozzle.Logger.Info("exiting", lager.Data{"signal caught": s.String()})
			err := nozzle.firehoseClient.CloseConsumer()
			if err != nil {
				nozzle.Logger.Error("error closing consumer", err)
			}
			os.Exit(1)
		case <-ticker.C:
			// get the pending as current
			currentEvents := pendingEvents
			// reset the pending events
			pendingEvents = make(map[string][]interface{})
			nozzle.GoroutineSem <- 1
			go nozzle.PostData(&currentEvents, true)
		case msg := <-nozzle.msgChan:
			nozzle.TotalEventsReceived++
			// process message
			var message Message
			var messageType = msg.GetEventType().String()
			switch msg.GetEventType() {
			// Metrics
			case events.Envelope_ValueMetric:
				if !nozzle.NozzleConfig.ExcludeMetricEvents {
					message = messages.NewValueMetric(msg, nozzle.cachingClient)
					pendingEvents[messageType] = append(pendingEvents[messageType], message)
				}
			case events.Envelope_CounterEvent:
				m := messages.NewCounterEvent(msg, nozzle.cachingClient)
				if strings.Contains(m.Name, "TruncatingBuffer.DroppedMessage") {
					nozzle.Logger.Error("received TruncatingBuffer alert", nil)
					nozzle.logSlowConsumerAlert()
				}
				if strings.Contains(m.Name, "doppler_proxy.slow_consumer") && m.Delta > 0 {
					nozzle.Logger.Error("received slow_consumer alert", nil)
					nozzle.logSlowConsumerAlert()
				}
				if !nozzle.NozzleConfig.ExcludeMetricEvents {
					message = m
					pendingEvents[messageType] = append(pendingEvents[messageType], message)
				}

			case events.Envelope_ContainerMetric:
				if !nozzle.NozzleConfig.ExcludeMetricEvents {
					message = messages.NewContainerMetric(msg, nozzle.cachingClient)
					if message != nil {
						pendingEvents[messageType] = append(pendingEvents[messageType], message)
					}
				}

			// Logs Errors
			case events.Envelope_LogMessage:
				if !nozzle.NozzleConfig.ExcludeLogEvents {
					message = messages.NewLogMessage(msg, nozzle.cachingClient)
					if message != nil {
						pendingEvents[messageType] = append(pendingEvents[messageType], message)
					}
				}

			case events.Envelope_Error:
				if !nozzle.NozzleConfig.ExcludeLogEvents {
					message = messages.NewError(msg, nozzle.cachingClient)
					pendingEvents[messageType] = append(pendingEvents[messageType], message)
				}

			// HTTP Start/Stop
			case events.Envelope_HttpStartStop:
				if !nozzle.NozzleConfig.ExcludeHttpEvents {
					message = messages.NewHTTPStartStop(msg, nozzle.cachingClient)
					if message != nil {
						pendingEvents[messageType] = append(pendingEvents[messageType], message)
					}
				}
			default:
				nozzle.Logger.Info("uncategorized message", lager.Data{"message": msg.String()})
				continue
			}
			// When the number of one type of events reaches the max per batch, trigger the post immediately
			doPost := false
			for _, v := range pendingEvents {
				if len(v) >= nozzle.NozzleConfig.MaxMsgNumPerBatch {
					doPost = true
					break
				}
			}
			if doPost {
				currentEvents := pendingEvents
				pendingEvents = make(map[string][]interface{})
				nozzle.GoroutineSem <- 1
				go nozzle.PostData(&currentEvents, true)
			}
		case err := <-nozzle.errChan:
			nozzle.Logger.Error("Error while reading from the firehose", err)

			if strings.Contains(err.Error(), "close 1008 (policy violation)") {
				nozzle.Logger.Error("Disconnected because nozzle couldn't keep up. Please try scaling up the nozzle.", nil)
				nozzle.logSlowConsumerAlert()
			}

			// post the buffered messages
			nozzle.GoroutineSem <- 1
			nozzle.PostData(&pendingEvents, true)

			nozzle.Logger.Error("Closing connection with traffic controller", nil)
			nozzle.firehoseClient.CloseConsumer()
			return err
		}
	}
}

// Log slowConsumerAlert as a ValueMetric event
func (nozzle *NozzleBase) logSlowConsumerAlert() {
	name := "slowConsumerAlert"
	value := float64(1)
	unit := "b"
	valueMetric := &events.ValueMetric{
		Name:  &name,
		Value: &value,
		Unit:  &unit,
	}

	timeStamp := time.Now().UnixNano()
	eventType := events.Envelope_ValueMetric
	job := "nozzle"
	origin := "alert"
	envelope := &events.Envelope{
		EventType:   &eventType,
		Timestamp:   &timeStamp,
		Job:         &job,
		Origin:      &origin,
		ValueMetric: valueMetric,
	}

	var message Message
	message = messages.NewValueMetric(envelope, nozzle.cachingClient)
	currentEvents := make(map[string][]interface{})
	currentEvents[eventType.String()] = append(currentEvents[eventType.String()], message)

	nozzle.GoroutineSem <- 1
	nozzle.PostData(&currentEvents, false)
}
