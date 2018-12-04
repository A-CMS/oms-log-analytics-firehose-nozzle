package ehnozzle

import (
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/caching"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/ehclient"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/firehose"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/messages"
	"github.com/cloudfoundry/sonde-go/events"
)

type EHNozzle struct {
	logger              lager.Logger
	errChan             <-chan error
	msgChan             <-chan *events.Envelope
	signalChan          chan os.Signal
	ehClient            ehclient.Client
	firehoseClient      firehose.Client
	nozzleConfig        *NozzleConfig
	goroutineSem        chan int // to control the number of active post goroutines
	cachingClient       caching.CachingClient
	totalEventsReceived uint64
	totalEventsSent     uint64
	totalEventsLost     uint64
	totalDataSent       uint64
	mutex               *sync.Mutex
}

type NozzleConfig struct {
	EHBatchTime           time.Duration
	EHMaxMsgNumPerBatch   int
	ExcludeMetricEvents   bool
	ExcludeLogEvents      bool
	ExcludeHttpEvents     bool
	LogEventCount         bool
	LogEventCountInterval time.Duration
}

func NewEventsHubsNozzle(logger lager.Logger, firehoseClient firehose.Client, ehClient ehclient.Client, nozzleConfig *NozzleConfig, caching caching.CachingClient) *EHNozzle {
	maxPostGoroutines := int(100000 / nozzleConfig.EHMaxMsgNumPerBatch)
	return &EHNozzle{
		logger:              logger,
		errChan:             make(<-chan error),
		msgChan:             make(<-chan *events.Envelope),
		signalChan:          make(chan os.Signal, 2),
		ehClient:            ehClient,
		firehoseClient:      firehoseClient,
		nozzleConfig:        nozzleConfig,
		goroutineSem:        make(chan int, maxPostGoroutines),
		cachingClient:       caching,
		totalEventsReceived: uint64(0),
		totalEventsSent:     uint64(0),
		totalEventsLost:     uint64(0),
		totalDataSent:       uint64(0),
		mutex:               &sync.Mutex{},
	}
}

func (o *EHNozzle) Start() error {
	o.cachingClient.Initialize(true)

	// setup for termination signal from CF
	signal.Notify(o.signalChan, syscall.SIGTERM, syscall.SIGINT)

	o.msgChan, o.errChan = o.firehoseClient.Connect()

	if o.nozzleConfig.LogEventCount {
		o.logTotalEvents(o.nozzleConfig.LogEventCountInterval)
	}
	err := o.routeEvents()
	return err
}

func (o *EHNozzle) logTotalEvents(interval time.Duration) {
	logEventCountTicker := time.NewTicker(interval)
	lastReceivedCount := uint64(0)
	lastSentCount := uint64(0)
	lastLostCount := uint64(0)

	go func() {
		for range logEventCountTicker.C {
			timeStamp := time.Now().UnixNano()
			totalReceivedCount := o.totalEventsReceived
			totalSentCount := o.totalEventsSent
			totalLostCount := o.totalEventsLost
			currentEvents := make(map[string][]interface{})

			// Generate CounterEvent
			o.addEventCountEvent("eventsReceived", totalReceivedCount-lastReceivedCount, totalReceivedCount, &timeStamp, &currentEvents)
			o.addEventCountEvent("eventsSent", totalSentCount-lastSentCount, totalSentCount, &timeStamp, &currentEvents)
			o.addEventCountEvent("eventsLost", totalLostCount-lastLostCount, totalLostCount, &timeStamp, &currentEvents)

			o.goroutineSem <- 1
			o.postData(&currentEvents, false)

			lastReceivedCount = totalReceivedCount
			lastSentCount = totalSentCount
			lastLostCount = totalLostCount
		}
	}()
}

func (o *EHNozzle) addEventCountEvent(name string, deltaCount uint64, count uint64, timeStamp *int64, currentEvents *map[string][]interface{}) {
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

	var omsMsg EHMessage
	eventTypeString := eventType.String()
	omsMsg = messages.NewCounterEvent(envelope, o.cachingClient)
	(*currentEvents)[eventTypeString] = append((*currentEvents)[eventTypeString], omsMsg)
}

func (o *EHNozzle) postData(events *map[string][]interface{}, addCount bool) {
	for k, v := range *events {
		if len(v) > 0 {
			o.logger.Debug("Posting to Event Hubs",
				lager.Data{"event type": k},
				lager.Data{"event count": len(v)})
			if size, err := o.ehClient.PostBatchData(&v, k); err != nil {
				o.logger.Error("error posting message to Event Hubs", err,
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)})
				if addCount {
					o.mutex.Lock()
					o.totalEventsLost += uint64(len(v))
					o.mutex.Unlock()
				}
			} else {
				if addCount {
					o.mutex.Lock()
					o.totalEventsSent += uint64(len(v))
					o.totalDataSent += uint64(size)
					o.mutex.Unlock()
				}
				break
			}
		}
	}
	<-o.goroutineSem
}

func (o *EHNozzle) routeEvents() error {
	pendingEvents := make(map[string][]interface{})
	// Firehose message processing loop
	ticker := time.NewTicker(o.nozzleConfig.EHBatchTime)
	for {
		// loop over message and signal channel
		select {
		case s := <-o.signalChan:
			o.logger.Info("exiting", lager.Data{"signal caught": s.String()})
			err := o.firehoseClient.CloseConsumer()
			if err != nil {
				o.logger.Error("error closing consumer", err)
			}
			os.Exit(1)
		case <-ticker.C:
			// get the pending as current
			currentEvents := pendingEvents
			// reset the pending events
			pendingEvents = make(map[string][]interface{})
			o.goroutineSem <- 1
			go o.postData(&currentEvents, true)
		case msg := <-o.msgChan:
			o.totalEventsReceived++
			// process message
			var ehMessage EHMessage
			var ehMessageType = msg.GetEventType().String()
			switch msg.GetEventType() {
			// Metrics
			case events.Envelope_ValueMetric:
				if !o.nozzleConfig.ExcludeMetricEvents {
					ehMessage = messages.NewValueMetric(msg, o.cachingClient)
					pendingEvents[ehMessageType] = append(pendingEvents[ehMessageType], ehMessage)
				}
			case events.Envelope_CounterEvent:
				m := messages.NewCounterEvent(msg, o.cachingClient)
				if strings.Contains(m.Name, "TruncatingBuffer.DroppedMessage") {
					o.logger.Error("received TruncatingBuffer alert", nil)
					o.logSlowConsumerAlert()
				}
				if strings.Contains(m.Name, "doppler_proxy.slow_consumer") && m.Delta > 0 {
					o.logger.Error("received slow_consumer alert", nil)
					o.logSlowConsumerAlert()
				}
				if !o.nozzleConfig.ExcludeMetricEvents {
					ehMessage = m
					pendingEvents[ehMessageType] = append(pendingEvents[ehMessageType], ehMessage)
				}

			case events.Envelope_ContainerMetric:
				if !o.nozzleConfig.ExcludeMetricEvents {
					ehMessage = messages.NewContainerMetric(msg, o.cachingClient)
					if ehMessage != nil {
						pendingEvents[ehMessageType] = append(pendingEvents[ehMessageType], ehMessage)
					}
				}

			// Logs Errors
			case events.Envelope_LogMessage:
				if !o.nozzleConfig.ExcludeLogEvents {
					ehMessage = messages.NewLogMessage(msg, o.cachingClient)
					if ehMessage != nil {
						pendingEvents[ehMessageType] = append(pendingEvents[ehMessageType], ehMessage)
					}
				}

			case events.Envelope_Error:
				if !o.nozzleConfig.ExcludeLogEvents {
					ehMessage = messages.NewError(msg, o.cachingClient)
					pendingEvents[ehMessageType] = append(pendingEvents[ehMessageType], ehMessage)
				}

			// HTTP Start/Stop
			case events.Envelope_HttpStartStop:
				if !o.nozzleConfig.ExcludeHttpEvents {
					ehMessage = messages.NewHTTPStartStop(msg, o.cachingClient)
					if ehMessage != nil {
						pendingEvents[ehMessageType] = append(pendingEvents[ehMessageType], ehMessage)
					}
				}
			default:
				o.logger.Info("uncategorized message", lager.Data{"message": msg.String()})
				continue
			}
			// When the number of one type of events reaches the max per batch, trigger the post immediately
			doPost := false
			for _, v := range pendingEvents {
				if len(v) >= o.nozzleConfig.EHMaxMsgNumPerBatch {
					doPost = true
					break
				}
			}
			if doPost {
				currentEvents := pendingEvents
				pendingEvents = make(map[string][]interface{})
				o.goroutineSem <- 1
				go o.postData(&currentEvents, true)
			}
		case err := <-o.errChan:
			o.logger.Error("Error while reading from the firehose", err)

			if strings.Contains(err.Error(), "close 1008 (policy violation)") {
				o.logger.Error("Disconnected because nozzle couldn't keep up. Please try scaling up the nozzle.", nil)
				o.logSlowConsumerAlert()
			}

			// post the buffered messages
			o.goroutineSem <- 1
			o.postData(&pendingEvents, true)

			o.logger.Error("Closing connection with traffic controller", nil)
			o.firehoseClient.CloseConsumer()
			return err
		}
	}
}

// Log slowConsumerAlert as a ValueMetric event to OMS
func (o *EHNozzle) logSlowConsumerAlert() {
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

	var omsMsg EHMessage
	omsMsg = messages.NewValueMetric(envelope, o.cachingClient)
	currentEvents := make(map[string][]interface{})
	currentEvents[eventType.String()] = append(currentEvents[eventType.String()], omsMsg)

	o.goroutineSem <- 1
	o.postData(&currentEvents, false)
}

// EHMessage is a marker inteface for JSON formatted messages published to OMS
type EHMessage interface{}
