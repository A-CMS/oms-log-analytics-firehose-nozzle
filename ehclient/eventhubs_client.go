package ehclient

import (
	"context"
	"encoding/json"
	"time"

	"code.cloudfoundry.org/lager"
	eventhub "github.com/Azure/azure-event-hubs-go"
)

type Client interface {
	PostData(*[]byte, string) error
	PostBatchData(*[]interface{}, string) (int, error)
}

// Client posts messages to Event Hubs
type ehclient struct {
	connectionString string
	logger           lager.Logger
}

// NewEventHubsClient creates a new instance of the Client
func NewEventHubsClient(connectionString string, logger lager.Logger) Client {
	return &ehclient{
		connectionString: connectionString,
		logger:           logger,
	}
}

func (c *ehclient) PostData(msg *[]byte, logType string) error {
	// Create context
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Create Event Hubs client
	hub, err := eventhub.NewHubFromConnectionString(c.connectionString)
	if err != nil {
		c.logger.Debug("Error creating Event Hub client")
		return err
	}
	defer hub.Close(ctx)

	// Send message
	err = hub.Send(ctx, eventhub.NewEventFromString(string(*msg)))
	if err != nil {
		c.logger.Debug("Error sending message to Event Hub")
		return err
	}

	return nil
}

func (c *ehclient) PostBatchData(batch *[]interface{}, logType string) (int, error) {
	// Create context
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Create Event Hubs client
	hub, err := eventhub.NewHubFromConnectionString(c.connectionString)
	if err != nil {
		c.logger.Debug("Error creating Event Hub client")
		return 0, err
	}
	defer hub.Close(ctx)

	// Assemble batch
	var evbatch []*eventhub.Event
	var evbatchSize int
	for _, v := range *batch {
		j, _ := json.Marshal(v)
		evbatchSize += len(j)
		evbatch = append(evbatch, eventhub.NewEventFromString(string(j)))
	}

	// Send batch
	c.logger.Debug("Sending batch to hub", lager.Data{"size": evbatchSize})
	err = hub.SendBatch(ctx, &eventhub.EventBatch{Events: evbatch})
	if err != nil {
		c.logger.Debug("Error sending message to Event Hub")
		return 0, err
	}

	return evbatchSize, nil
}
