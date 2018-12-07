package omsnozzle

import (
	"encoding/json"
	"time"

	"code.cloudfoundry.org/lager"
	"github.com/Azure/oms-log-analytics-firehose-nozzle/common"
)

// OMSNozzle type.
type OMSNozzle struct {
	base          *common.NozzleBase
	omsTypePrefix string
}

// NewOMSNozzle creates an OMS nozzle.
func NewOMSNozzle(nozzle *common.NozzleBase, omsTypePrefix string) common.Nozzle {
	n := OMSNozzle{
		base:          nozzle,
		omsTypePrefix: omsTypePrefix,
	}
	n.base.PostData = n.postData
	return &n
}

// Start starts the nozzle.
func (o *OMSNozzle) Start() error {
	return o.base.Start()
}

// PostData posts the data.
func (o *OMSNozzle) postData(events *map[string][]interface{}, addCount bool) {
	for k, v := range *events {
		if len(v) > 0 {
			if msgAsJSON, err := json.Marshal(&v); err != nil {
				o.base.Logger.Error("error marshalling message to JSON", err,
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)})
			} else {
				o.base.Logger.Debug("Posting to OMS",
					lager.Data{"event type": k},
					lager.Data{"event count": len(v)},
					lager.Data{"total size": len(msgAsJSON)})
				if len(o.omsTypePrefix) > 0 {
					k = o.omsTypePrefix + k
				}
				nRetries := 4
				for nRetries > 0 {
					requestStartTime := time.Now()
					if err = o.base.Client.PostData(&msgAsJSON, k); err != nil {
						nRetries--
						elapsedTime := time.Since(requestStartTime)
						o.base.Logger.Error("error posting message to OMS", err,
							lager.Data{"event type": k},
							lager.Data{"elapse time": elapsedTime.String()},
							lager.Data{"event count": len(v)},
							lager.Data{"total size": len(msgAsJSON)},
							lager.Data{"remaining attempts": nRetries})
						time.Sleep(time.Second * 1)
					} else {
						if addCount {
							o.base.Mutex.Lock()
							o.base.TotalEventsSent += uint64(len(v))
							o.base.TotalDataSent += uint64(len(msgAsJSON))
							o.base.Mutex.Unlock()
						}
						break
					}
				}
				if nRetries == 0 && addCount {
					o.base.Mutex.Lock()
					o.base.TotalEventsLost += uint64(len(v))
					o.base.Mutex.Unlock()
				}
			}
		}
	}
	<-o.base.GoroutineSem
}
