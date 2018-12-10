package omsnozzle

import (
	"encoding/json"
	"time"

	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/common"
)

// OMSNozzle type.
type OMSNozzle struct {
	*common.NozzleBase
	omsTypePrefix string
}

// NewOMSNozzle creates an OMS nozzle.
func NewOMSNozzle(nozzle *common.NozzleBase, omsTypePrefix string) common.Nozzle {
	n := OMSNozzle{
		NozzleBase:    nozzle,
		omsTypePrefix: omsTypePrefix,
	}
	n.NozzleBase.PostData = n.postData
	return &n
}

// PostData posts the data.
func (o *OMSNozzle) postData(events *map[string][]interface{}, addCount bool) {
	for k, v := range *events {
		if len(v) > 0 {
			if msgAsJSON, err := json.Marshal(&v); err != nil {
				o.Logger.Error("error marshalling message to JSON", err,
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)})
			} else {
				o.Logger.Debug("Posting to OMS",
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)},
					lager.Data{"total size": len(msgAsJSON)})
				if len(o.omsTypePrefix) > 0 {
					k = o.omsTypePrefix + k
				}
				nRetries := 4
				for nRetries > 0 {
					requestStartTime := time.Now()
					if err = o.Client.PostData(&msgAsJSON, k); err != nil {
						nRetries--
						elapsedTime := time.Since(requestStartTime)
						o.Logger.Error("error posting message to OMS", err,
							lager.Data{"event type": k},
							lager.Data{"elapse time": elapsedTime.String()},
							lager.Data{"event count": len(v)},
							lager.Data{"total size": len(msgAsJSON)},
							lager.Data{"remaining attempts": nRetries})
						time.Sleep(time.Second * 1)
					} else {
						if addCount {
							o.Mutex.Lock()
							o.TotalEventsSent += uint64(len(v))
							o.TotalDataSent += uint64(len(msgAsJSON))
							o.Mutex.Unlock()
						}
						break
					}
				}
				if nRetries == 0 && addCount {
					o.Mutex.Lock()
					o.TotalEventsLost += uint64(len(v))
					o.Mutex.Unlock()
				}
			}
		}
	}
	<-o.GoroutineSem
}
