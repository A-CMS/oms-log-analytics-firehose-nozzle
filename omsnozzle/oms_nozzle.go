package omsnozzle

import (
	"encoding/json"
	"time"

	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/common"
)

type OmsLogProvider struct {
	nozzle        *common.Nozzle
	omsTypePrefix string
}

func NewOmsLogProvider(nozzle *common.Nozzle, omsTypePrefix string) common.NozzleLogProvider {
	return &OmsLogProvider{
		nozzle:        nozzle,
		omsTypePrefix: omsTypePrefix,
	}
}

func (o *OmsLogProvider) PostData(events *map[string][]interface{}, addCount bool) {
	for k, v := range *events {
		if len(v) > 0 {
			if msgAsJson, err := json.Marshal(&v); err != nil {
				o.nozzle.Logger.Error("error marshalling message to JSON", err,
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)})
			} else {
				o.nozzle.Logger.Debug("Posting to OMS",
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)},
					lager.Data{"total size": len(msgAsJson)})
				if len(o.omsTypePrefix) > 0 {
					k = o.omsTypePrefix + k
				}
				nRetries := 4
				for nRetries > 0 {
					requestStartTime := time.Now()
					if err = o.nozzle.Client.PostData(&msgAsJson, k); err != nil {
						nRetries--
						elapsedTime := time.Since(requestStartTime)
						o.nozzle.Logger.Error("error posting message to OMS", err,
							lager.Data{"event type": k},
							lager.Data{"elapse time": elapsedTime.String()},
							lager.Data{"event count": len(v)},
							lager.Data{"total size": len(msgAsJson)},
							lager.Data{"remaining attempts": nRetries})
						time.Sleep(time.Second * 1)
					} else {
						if addCount {
							o.nozzle.Mutex.Lock()
							o.nozzle.TotalEventsSent += uint64(len(v))
							o.nozzle.TotalDataSent += uint64(len(msgAsJson))
							o.nozzle.Mutex.Unlock()
						}
						break
					}
				}
				if nRetries == 0 && addCount {
					o.nozzle.Mutex.Lock()
					o.nozzle.TotalEventsLost += uint64(len(v))
					o.nozzle.Mutex.Unlock()
				}
			}
		}
	}
	<-o.nozzle.GoroutineSem
}
