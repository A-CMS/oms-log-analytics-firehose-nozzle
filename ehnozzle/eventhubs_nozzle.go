package ehnozzle

import (
	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/common"
)

// EventHubsNozzle type.
type EventHubsNozzle struct {
	base *common.NozzleBase
}

// NewEventHubsNozzle creates an Event Hubs nozzle.
func NewEventHubsNozzle(nozzle *common.NozzleBase) common.Nozzle {
	n := EventHubsNozzle{
		base: nozzle,
	}
	n.base.PostData = n.postData
	return &n
}

// Start starts the nozzle.
func (o *EventHubsNozzle) Start() error {
	return o.base.Start()
}

// PostData posts the data.
func (o *EventHubsNozzle) postData(events *map[string][]interface{}, addCount bool) {
	for k, v := range *events {
		if len(v) > 0 {
			o.base.Logger.Debug("Posting to Event Hubs",
				lager.Data{"event type": k},
				lager.Data{"event count": len(v)})
			if size, err := o.base.Client.PostBatchData(&v, k); err != nil {
				o.base.Logger.Error("error posting message to Event Hubs", err,
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)})
				if addCount {
					o.base.Mutex.Lock()
					o.base.TotalEventsLost += uint64(len(v))
					o.base.Mutex.Unlock()
				}
			} else {
				if addCount {
					o.base.Mutex.Lock()
					o.base.TotalEventsSent += uint64(len(v))
					o.base.TotalDataSent += uint64(size)
					o.base.Mutex.Unlock()
				}
				break
			}
		}
	}
	<-o.base.GoroutineSem
}
