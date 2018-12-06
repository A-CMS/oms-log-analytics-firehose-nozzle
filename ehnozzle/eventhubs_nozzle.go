package ehnozzle

import (
	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/common"
)

type EHLogProvider struct {
	nozzle *common.Nozzle
}

func NewEHLogProvider(nozzle *common.Nozzle) common.NozzleLogProvider {
	return &EHLogProvider{
		nozzle: nozzle,
	}
}

func (o *EHLogProvider) PostData(events *map[string][]interface{}, addCount bool) {
	for k, v := range *events {
		if len(v) > 0 {
			o.nozzle.Logger.Debug("Posting to Event Hubs",
				lager.Data{"event type": k},
				lager.Data{"event count": len(v)})
			if size, err := o.nozzle.Client.PostBatchData(&v, k); err != nil {
				o.nozzle.Logger.Error("error posting message to Event Hubs", err,
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)})
				if addCount {
					o.nozzle.Mutex.Lock()
					o.nozzle.TotalEventsLost += uint64(len(v))
					o.nozzle.Mutex.Unlock()
				}
			} else {
				if addCount {
					o.nozzle.Mutex.Lock()
					o.nozzle.TotalEventsSent += uint64(len(v))
					o.nozzle.TotalDataSent += uint64(size)
					o.nozzle.Mutex.Unlock()
				}
				break
			}
		}
	}
	<-o.nozzle.GoroutineSem
}
