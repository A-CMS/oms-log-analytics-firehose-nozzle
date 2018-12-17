package ehclient

import (
	"context"
	"encoding/json"
	"time"

	"code.cloudfoundry.org/lager"
	eventhub "github.com/Azure/azure-event-hubs-go"
	common "github.com/Azure/oms-log-analytics-firehose-nozzle/common"
)

// Client posts messages to Event Hubs
type ehclient struct {
	connectionString string
	hub              *eventhub.Hub
	logger           lager.Logger
}

// NewEventHubsClient creates a new instance of the Client
func NewEventHubsClient(connectionString string, logger lager.Logger) common.Client {
	// Create Event Hubs client
	hub, err := eventhub.NewHubFromConnectionString(connectionString)
	if err != nil {
		logger.Error("ehclient: error creating Event Hubs client", err)
		return nil
	}

	//defer hub.Close(context.Background()) //??????

	// Return client
	return &ehclient{
		connectionString: connectionString,
		hub:              hub,
		logger:           logger,
	}
}

func (c *ehclient) PostData(msg *[]byte, logType string) error {
	// Create context
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Send message
	err := c.hub.Send(ctx, eventhub.NewEventFromString(string(*msg)))
	if err != nil {
		c.logger.Debug("Error sending message to Event Hub")
		return err
	}

	return nil
}

func (c *ehclient) PostBatchData(batch *[]interface{}, logType string) (int, error) {
	// Assemble batch
	var evbatch []*eventhub.Event
	var evbatchSize int
	for _, v := range *batch {
		j, _ := json.Marshal(v)
		evbatchSize += len(j)
		evbatch = append(evbatch, eventhub.NewEventFromString(string(j)))
	}

	// Create context
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Send batch
	c.logger.Debug("Sending batch to hub", lager.Data{"size": evbatchSize})
	err := c.hub.SendBatch(ctx, &eventhub.EventBatch{Events: evbatch})
	if err != nil {
		c.logger.Error("ehclient: error sending batch to Event Hub", err)
		return 0, err
	}

	return evbatchSize, nil
}
