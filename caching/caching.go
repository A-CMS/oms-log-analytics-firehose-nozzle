package caching

import (
	"fmt"
	"os"

	"code.cloudfoundry.org/lager"
	cfclient "github.com/cloudfoundry-community/go-cfclient"
)

type AppInfo struct {
	Name    string
	Org     string
	OrgID   string
	Space   string
	SpaceID string
}

type Caching struct {
	cfClientConfig *cfclient.Config
	appInfosByGuid map[string]AppInfo
	logger         lager.Logger
	instanceName   string
	environment    string
}

type CachingClient interface {
	GetAppInfo(string) AppInfo
	GetInstanceName() string
	GetEnvironmentName() string
	Initialize()
}

func NewCaching(config *cfclient.Config, logger lager.Logger, environment string) CachingClient {
	return &Caching{
		cfClientConfig: config,
		appInfosByGuid: make(map[string]AppInfo),
		logger:         logger,
		environment:    environment,
	}
}

func (c *Caching) Initialize() {
	c.setInstanceName()

	cfClient, err := cfclient.NewClient(c.cfClientConfig)
	if err != nil {
		c.logger.Fatal("error creating cfclient", err)
	}

	apps, err := cfClient.ListApps()
	if err != nil {
		c.logger.Fatal("error getting app list", err)
	}

	for _, app := range apps {
		var appInfo = AppInfo{
			Name:    app.Name,
			Org:     app.SpaceData.Entity.OrgData.Entity.Name,
			OrgID:   app.SpaceData.Entity.OrgData.Entity.Guid,
			Space:   app.SpaceData.Entity.Name,
			SpaceID: app.SpaceData.Entity.Guid,
		}
		c.appInfosByGuid[app.Guid] = appInfo
		c.logger.Info("adding to app name cache",
			lager.Data{"guid": app.Guid},
			lager.Data{"name": appInfo.Name},
			lager.Data{"org": appInfo.Org},
			lager.Data{"space": appInfo.Space},
			lager.Data{"cache size": len(c.appInfosByGuid)})
	}
}

func (c *Caching) GetAppInfo(appGuid string) AppInfo {
	if appInfo, ok := c.appInfosByGuid[appGuid]; ok {
		return appInfo
	} else {
		c.logger.Info("App info not found for GUID",
			lager.Data{"guid": appGuid},
			lager.Data{"app name cache size": len(c.appInfosByGuid)})
		// call the client api to get the name for this app
		// purposely create a new client due to issue in using a single client
		cfClient, err := cfclient.NewClient(c.cfClientConfig)
		if err != nil {
			c.logger.Error("error creating cfclient", err)
			return AppInfo{
				Name:    "",
				Org:     "",
				OrgID:   "",
				Space:   "",
				SpaceID: "",
			}
		}
		app, err := cfClient.AppByGuid(appGuid)
		if err != nil {
			c.logger.Error("error getting app info", err, lager.Data{"guid": appGuid})
			return AppInfo{
				Name:    "",
				Org:     "",
				OrgID:   "",
				Space:   "",
				SpaceID: "",
			}
		} else {
			// store app info in map
			appInfo = AppInfo{
				Name:    app.Name,
				Org:     app.SpaceData.Entity.OrgData.Entity.Name,
				OrgID:   app.SpaceData.Entity.OrgData.Entity.Guid,
				Space:   app.SpaceData.Entity.Name,
				SpaceID: app.SpaceData.Entity.Guid,
			}
			appInfo = c.appInfosByGuid[app.Guid]
			c.logger.Info("adding to app name cache",
				lager.Data{"guid": app.Guid},
				lager.Data{"name": appInfo.Name},
				lager.Data{"org": appInfo.Org},
				lager.Data{"space": appInfo.Space},
				lager.Data{"cache size": len(c.appInfosByGuid)})
			// return the app name
			return appInfo
		}
	}
}

func (c *Caching) setInstanceName() error {
	// instance id to track multiple nozzles, used for logging
	hostName, err := os.Hostname()
	if err != nil {
		c.logger.Error("failed to get hostname for nozzle instance", err)
		c.instanceName = fmt.Sprintf("pid-%d", os.Getpid())
	} else {
		c.instanceName = fmt.Sprintf("pid-%d@%s", os.Getpid(), hostName)
	}
	c.logger.Info("getting nozzle instance name", lager.Data{"name": c.instanceName})
	return err
}

func (c *Caching) GetInstanceName() string {
	return c.instanceName
}

func (c *Caching) GetEnvironmentName() string {
	return c.environment
}
