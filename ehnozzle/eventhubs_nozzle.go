package ehnozzle

import (
	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/common"
)

// EventHubsNozzle type.
type EventHubsNozzle struct {
	*common.NozzleBase
}

// NewEventHubsNozzle creates an Event Hubs nozzle.
func NewEventHubsNozzle(nozzle *common.NozzleBase) common.Nozzle {
	n := EventHubsNozzle{
		NozzleBase: nozzle,
	}
	n.NozzleBase.PostData = n.postData
	return &n
}

// PostData posts the data.
func (o *EventHubsNozzle) postData(events *map[string][]interface{}, addCount bool) {
	for k, v := range *events {
		if len(v) > 0 {
			o.Logger.Debug("Posting to Event Hubs",
				lager.Data{"event type": k},
				lager.Data{"event count": len(v)})
			if size, err := o.Client.PostBatchData(&v, k); err != nil {
				o.Logger.Error("error posting message to Event Hubs", err,
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)})
				if addCount {
					o.Mutex.Lock()
					o.TotalEventsLost += uint64(len(v))
					o.Mutex.Unlock()
				}
			} else {
				if addCount {
					o.Mutex.Lock()
					o.TotalEventsSent += uint64(len(v))
					o.TotalDataSent += uint64(size)
					o.Mutex.Unlock()
				}
				break
			}
		}
	}
	<-o.GoroutineSem
}
