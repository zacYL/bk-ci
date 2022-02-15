/*
 * Copyright (c) 2021 THL A29 Limited, a Tencent company. All rights reserved
 *
 * This source code file is licensed under the MIT License, you may obtain a copy of the License at
 *
 * http://opensource.org/licenses/MIT
 *
 */

package crm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/bk-ci/src/booster/server/pkg/rd"
	dcmac "github.com/Tencent/bk-ci/src/booster/server/pkg/resource/crm/operator/dc_mac"

	"github.com/Tencent/bk-ci/src/booster/common/blog"
	"github.com/Tencent/bk-ci/src/booster/common/codec"
	commonMySQL "github.com/Tencent/bk-ci/src/booster/common/mysql"
	"github.com/Tencent/bk-ci/src/booster/server/config"
	"github.com/Tencent/bk-ci/src/booster/server/pkg/engine"
	rsc "github.com/Tencent/bk-ci/src/booster/server/pkg/resource"
	op "github.com/Tencent/bk-ci/src/booster/server/pkg/resource/crm/operator"
	"github.com/Tencent/bk-ci/src/booster/server/pkg/resource/crm/operator/k8s"
	"github.com/Tencent/bk-ci/src/booster/server/pkg/resource/crm/operator/mesos"
	"github.com/Tencent/bk-ci/src/booster/server/pkg/types"
)

// NewResourceManager get a new container resource manager.
func NewResourceManager(
	c *config.ContainerResourceConfig,
	event types.RoleChangeEvent,
	rdClient rd.RegisterDiscover) (ResourceManager, error) {
	var operator op.Operator
	var err error

	switch c.Operator {
	case config.CRMOperatorMesos:
		if operator, err = mesos.NewOperator(c); err != nil {
			blog.Errorf("crm: new resource manager get operator(%s) failed: %v", c.Operator, err)
			return nil, err
		}
	case config.CRMOperatorK8S:
		if operator, err = k8s.NewOperator(c); err != nil {
			blog.Errorf("crm: new resource manager get operator(%s) failed: %v", c.Operator, err)
			return nil, err
		}
	case config.CRMOperatorDCMac:
		if operator, err = dcmac.NewOperator(c, rdClient); err != nil {
			blog.Errorf("crm: new resource manager get operator(%s) failed: %v", c.Operator, err)
			return nil, err
		}
	default:
		blog.Errorf("crm: new resource manager get operator(%s) failed: unknown operator", c.Operator)
		return nil, fmt.Errorf("unknown operator")
	}

	mysql, err := NewMySQL(MySQLConf{
		MySQLStorage:     c.MySQLStorage,
		MySQLDatabase:    c.MySQLDatabase,
		MySQLTable:       c.MySQLTable,
		MySQLUser:        c.MySQLUser,
		MySQLPwd:         c.MySQLPwd,
		MysqlTableOption: c.MysqlTableOption,
		SkipEnsure:       c.MysqlSkipEnsure,
	})
	if err != nil {
		blog.Errorf("crm: new resource manager get mysql failed: %v", err)
		return nil, err
	}

	return &resourceManager{
		conf:            c,
		event:           event,
		running:         false,
		operator:        operator,
		mysql:           mysql,
		handlerMap:      make(map[string]HandlerWithUser, 10),
		resourceLockMap: make(map[string]*lock, 50000),
		brokerSet:       NewBrokerSet(),
	}, nil
}

// ResourceManager define a resource manager which provides multi-user handlers to operate container resources.
type ResourceManager interface {
	// RegisterUser receive a user and return a HandlerWithUser which operates under this user.
	RegisterUser(user string) (HandlerWithUser, error)

	// GetResourceDetail return details of resource description.
	GetResourceDetail() *rsc.Details

	// Run the manager.
	Run() error
}

// HandlerWithUser define all operations supported by resource manager.
type HandlerWithUser interface {
	Init(resourceID string, param ResourceParam) error
	Launch(resourceID, city string, function op.InstanceFilterFunction) error
	Scale(resourceID string, function op.InstanceFilterFunction) error
	GetServiceInfo(resourceID string) (*op.ServiceInfo, error)
	IsServicePreparing(resourceID string) (bool, error)
	Release(resourceID string) error
	AddBroker(name string, strategyType StrategyType, strategy BrokerStrategy,
		param BrokerParam) error
	GetInstanceType(platform, group string) *config.InstanceType
}

// ResourceParam describe the request parameters to container resource manager.
type ResourceParam struct {
	City string `json:"city"`

	Platform string `json:"platform"`

	// env key-values which will be inserted into containers
	Env map[string]string `json:"env"`

	// ports that implements with port_name:protocol
	// such as my_port_alpha:http, my_port_beta:tcp
	// port numbers are all generated by container scheduler with cnm HOST
	Ports map[string]string `json:"ports"`

	// volumes implements the hostPath volumes with name:settings
	Volumes map[string]op.BcsVolume

	// container images
	Image string `json:"image"`

	// if it is a broker resource, then broker name should be specified
	BrokerName string `json:"broker_name"`
}

const (
	ServiceStatusStaging = op.ServiceStatusStaging
	ServiceStatusRunning = op.ServiceStatusRunning
	ServiceStatusFailed  = op.ServiceStatusFailed
)

// InstanceFilterFunction describe the function that decide how many instance to launch/scale.
type InstanceFilterFunction func(availableInstance int) (int, error)

type resourceManager struct {
	conf  *config.ContainerResourceConfig
	event types.RoleChangeEvent

	running bool
	ctx     context.Context
	cancel  context.CancelFunc

	operator op.Operator
	mysql    MySQL

	handlerMap     map[string]HandlerWithUser
	handlerMapLock sync.Mutex

	resourceLockMap     map[string]*lock
	resourceLockMapLock sync.RWMutex

	brokerSet *BrokerSet

	// following should be init in recover
	registeredResourceMap     map[string]*resource
	registeredResourceMapLock sync.Mutex

	nodeInfoPool *op.NodeInfoPool

	rscDetail []*rsc.RscDetails
	appDetail []*rsc.AppDetails
}

const (
	checkerTimeGap         = 1 * time.Second
	brokerCheckerTimeGap   = 1 * time.Second
	brokerCheckerSleepTime = 10 * time.Second
	syncTimeGap            = 1 * time.Second
	statsLogTimeGap        = 10 * time.Second
	lockCleanTimeGap       = 10 * time.Minute
	syncRscDetailTimeGap   = 1 * time.Second
	syncAppDetailTimeGap   = 1 * time.Second
)

type lock struct {
	sync.Mutex
	createAt time.Time
	lastHold time.Time
}

// RegisterUser register a new HandlerWithUser or get an existing one.
func (rm *resourceManager) RegisterUser(user string) (HandlerWithUser, error) {
	return rm.registerUser(user)
}

// GetResourceDetail get the details of all resources in pool.
func (rm *resourceManager) GetResourceDetail() *rsc.Details {
	return &rsc.Details{
		Rsc: rm.rscDetail,
		App: rm.appDetail,
	}
}

// Run the resource manager. Listen to role change events. Start manager when master, stop when not.
func (rm *resourceManager) Run() error {
	blog.Infof("crm: run the resource manager")

	for {
		blog.Infof("crm: ready to receive role change event")
		select {
		case e := <-rm.event:
			blog.Infof("crm: receive new role change event: %s", e)
			switch e {
			case types.ServerMaster:
				rm.start()
			case types.ServerSlave, types.ServerUnknown:
				rm.stop()
			default:
				blog.Warnf("crm: unknown role, will not change manager state: %s", e)
			}
		}
	}
}

func (rm *resourceManager) start() {
	blog.Infof("crm: start handler")
	if rm.running {
		blog.Errorf("crm: handler has already started")
		return
	}

	if err := rm.recover(); err != nil {
		blog.Errorf("crm: start handler recover failed: %v", err)
		return
	}

	rm.running = true
	rm.ctx, rm.cancel = context.WithCancel(context.Background())
	if err := rm.brokerSet.Recover(); err != nil {
		blog.Errorf("crm: start handler recover broker set failed: %v", err)
		return
	}
	go rm.runAllTracer()
	go rm.runSync()
	go rm.runLockCleaner()
	go rm.runBrokerChecker()
	go rm.runRscDetailSync()
	go rm.runAppDetailSync()
}

func (rm *resourceManager) stop() {
	blog.Infof("crm: stop handler")
	if !rm.running {
		blog.Errorf("crm: handler has already stopped")
		return
	}

	rm.cancel()
	rm.running = false
}

func (rm *resourceManager) runAllTracer() {
	blog.Infof("crm: begin to run all tracer")
	rm.registeredResourceMapLock.Lock()
	defer rm.registeredResourceMapLock.Unlock()

	for _, r := range rm.registeredResourceMap {
		if r.status == resourceStatusDeploying {
			go rm.trace(r.resourceID, r.user)
		}
	}
}

func (rm *resourceManager) runSync() {
	blog.Infof("crm: begin to run sync")
	ticker := time.NewTicker(syncTimeGap)
	defer ticker.Stop()

	logTicker := time.NewTicker(statsLogTimeGap)
	defer logTicker.Stop()

	for {
		select {
		case <-rm.ctx.Done():
			blog.Warnf("crm: sync done")
			return
		case <-logTicker.C:
			rm.logResourceStats()
		case <-ticker.C:
			rm.sync()
		}
	}
}

func (rm *resourceManager) runLockCleaner() {
	blog.Infof("crm: begin to run lock cleaner")
	ticker := time.NewTicker(lockCleanTimeGap)
	defer ticker.Stop()

	for {
		select {
		case <-rm.ctx.Done():
			blog.Warnf("crm: lock cleaner done")
			return
		case <-ticker.C:
			rm.resourceLockMapLock.Lock()
			num := 0
			for key, lock := range rm.resourceLockMap {
				if lock.createAt.Add(24 * time.Hour).Before(time.Now().Local()) {
					num++
					delete(rm.resourceLockMap, key)
				}
			}
			blog.Infof("crm: clean %d lock", num)
			rm.resourceLockMapLock.Unlock()
		}
	}
}

func (rm *resourceManager) runRscDetailSync() {
	blog.Infof("crm: begin to run rsc detail sync")
	ticker := time.NewTimer(syncRscDetailTimeGap)
	defer ticker.Stop()

	for {
		select {
		case <-rm.ctx.Done():
			blog.Warnf("crm: rsc detail sync done")
			return
		case <-ticker.C:
			if rm.nodeInfoPool == nil {
				continue
			}
			rm.rscDetail = rm.nodeInfoPool.GetDetail()
		}
	}
}

func (rm *resourceManager) runAppDetailSync() {
	blog.Infof("crm: begin to run app detail sync")
	ticker := time.NewTimer(syncAppDetailTimeGap)
	defer ticker.Stop()

	for {
		select {
		case <-rm.ctx.Done():
			blog.Warnf("crm: app detail sync done")
			return
		case <-ticker.C:
			if rm.registeredResourceMap == nil {
				continue
			}

			appDetail := make([]*rsc.AppDetails, 0, 100)
			rm.registeredResourceMapLock.Lock()
			for _, r := range rm.registeredResourceMap {
				if r.status == resourceStatusReleased {
					continue
				}

				appDetail = append(appDetail, &rsc.AppDetails{
					ResourceID:       r.resourceID,
					BrokerID:         r.brokerResourceID,
					BrokerName:       r.brokerName,
					BrokerSold:       r.brokerSold,
					User:             r.user,
					Status:           r.status.String(),
					Image:            r.param.Image,
					CreateTime:       r.initTime,
					RequestInstance:  r.requestInstance,
					NotReadyInstance: r.noReadyInstance,
					Label:            r.param.City,
				})
			}
			rm.registeredResourceMapLock.Unlock()
			rm.appDetail = appDetail
		}
	}
}

func (rm *resourceManager) logResourceStats() {
	blog.Infof("crm: report resources(%s) following: %s", rm.conf.Operator, rm.nodeInfoPool.GetStats())
}

func (rm *resourceManager) recover() error {
	blog.Infof("crm: run recover resources from databases")
	rl, err := rm.listResources(resourceStatusInit, resourceStatusDeploying, resourceStatusRunning)
	if err != nil {
		blog.Errorf("crm: recover resource failed: %v", err)
		return err
	}

	rm.nodeInfoPool = op.NewNodeInfoPool(rm.conf.BcsCPUPerInstance, rm.conf.BcsMemPerInstance, 1, rm.conf.InstanceType)

	rm.registeredResourceMapLock.Lock()
	defer rm.registeredResourceMapLock.Unlock()

	rm.registeredResourceMap = make(map[string]*resource, 1000)
	for _, r := range rl {
		rm.registeredResourceMap[r.resourceID] = r

		if r.noReadyInstance <= 0 {
			continue
		}

		// recover the no-ready records
		rm.nodeInfoPool.RecoverNoReadyBlock(r.resourceBlockKey, r.noReadyInstance)
		blog.Infof("crm: recover no-ready-instance(%d) from resource(%s)", r.noReadyInstance, r.resourceID)
	}

	return nil
}

func (rm *resourceManager) sync() {
	nodeInfoList, err := rm.operator.GetResource(rm.conf.BcsClusterID)
	if err != nil {
		blog.Errorf("crm: sync resource failed: %v", err)
		return
	}

	rm.nodeInfoPool.UpdateResources(nodeInfoList)
	blog.V(5).Infof(rm.nodeInfoPool.GetStats())
}

func (rm *resourceManager) trace(resourceID, user string) {
	blog.Infof("crm: begin to trace resource(%s) user(%s) until it finish deploying", resourceID, user)
	ticker := time.NewTicker(checkerTimeGap)
	defer ticker.Stop()

	for {
		select {
		case <-rm.ctx.Done():
			blog.Warnf("crm: resource(%s) user(%s) trace done", resourceID, user)
			return
		case <-ticker.C:
			if rm.isFinishDeploying(resourceID, user) {
				blog.Infof("crm: resource(%s) user(%s) finish deploying, checker exit", resourceID, user)
				return
			}
		}
	}
}

func (rm *resourceManager) isFinishDeploying(resourceID, user string) bool {
	info, err := rm.getServiceInfo(resourceID, user)

	// if resource no exist, means target service already deleted, just finish the deploying trace.
	if err == ErrorResourceNoExist {
		return true
	}

	if err != nil {
		blog.Errorf("crm: check if resource(%s) user(%s) is finish deploying, "+
			"get server status failed: %v, exit", resourceID, user, err)
		return false
	}

	switch info.Status {
	case ServiceStatusRunning, ServiceStatusFailed:
		blog.Infof("crm: check isFinishDeploying resource(%s) user(%s) finish deploying", resourceID, user)
		return true
	default:
		return false
	}
}

func (rm *resourceManager) freshDeployingStatus(resourceID, user string, ready int, terminated bool) {
	rm.lockResource(resourceID)
	defer rm.unlockResource(resourceID)

	blog.V(5).Infof("crm: try to fresh deploying status for resource(%s) user(%s) ready(%d) terminated(%v)",
		resourceID, user, ready, terminated)

	r, err := rm.getResources(resourceID)
	if err != nil {
		blog.Errorf("crm: fresh resource(%s) user(%s) deploying status, get resource failed: %v, exit",
			resourceID, user, err)
		return
	}

	if terminated {
		go rm.releaseNoReadyInstance(r.resourceBlockKey, r.noReadyInstance)
		r.noReadyInstance = 0
	} else {
		// the newest no ready num = resource request instance - the newest ready num
		currentNoReady := r.requestInstance - ready
		if r.noReadyInstance > currentNoReady && currentNoReady >= 0 {
			go rm.releaseNoReadyInstance(r.resourceBlockKey, r.noReadyInstance-currentNoReady)
			r.noReadyInstance = currentNoReady
		}
	}

	if terminated && r.status == resourceStatusDeploying {
		r.status = resourceStatusRunning
	}

	if err = rm.saveResources(r); err != nil {
		blog.Errorf("crm: fresh resource(%s) user(%s) deploying status, save resource failed: %v, exit",
			resourceID, user, err)
		return
	}

	blog.V(5).Infof(
		"crm: success to fresh deploying status for resource(%s) user(%s) ready(%d) terminated(%v)",
		resourceID, user, ready, terminated,
	)
}

func (rm *resourceManager) registerUser(user string) (HandlerWithUser, error) {
	rm.handlerMapLock.Lock()
	defer rm.handlerMapLock.Unlock()

	_, ok := rm.handlerMap[user]
	if !ok {
		rm.handlerMap[user] = &handlerWithUser{
			user: user,
			mgr:  rm,
		}
	}

	return rm.handlerMap[user], nil
}

func (rm *resourceManager) lockResource(resourceID string) {
	defer blog.V(5).Infof("crm: lock resource(%s)", resourceID)

	rm.resourceLockMapLock.RLock()
	mutex, ok := rm.resourceLockMap[resourceID]
	rm.resourceLockMapLock.RUnlock()
	if ok {
		mutex.Lock()
		mutex.lastHold = time.Now().Local()
		return
	}

	rm.resourceLockMapLock.Lock()
	mutex, ok = rm.resourceLockMap[resourceID]
	if !ok {
		blog.Info("crm: create resource lock(%s), current lock num(%d)", resourceID, len(rm.resourceLockMap))
		mutex = &lock{
			createAt: time.Now().Local(),
		}
		rm.resourceLockMap[resourceID] = mutex
	}
	rm.resourceLockMapLock.Unlock()

	mutex.Lock()
	mutex.lastHold = time.Now().Local()
}

func (rm *resourceManager) unlockResource(resourceID string) {
	rm.resourceLockMapLock.RLock()
	mutex, ok := rm.resourceLockMap[resourceID]
	rm.resourceLockMapLock.RUnlock()
	if !ok {
		blog.Errorf("try to unlock a no exist resource")
		return
	}

	// log a warning when the lock is hold for too long.
	now := time.Now().Local()
	if mutex.lastHold.Add(1 * time.Second).Before(now) {
		blog.Warnf("crm: resource(%s) lock hold for too long: %s", resourceID, now.Sub(mutex.lastHold).String())
	}
	blog.V(5).Infof("crm: unlock resource(%s)", resourceID)
	mutex.Unlock()
}

func (rm *resourceManager) init(resourceID, user string, param ResourceParam) error {
	if !rm.running {
		return ErrorManagerNotRunning
	}

	rm.registeredResourceMapLock.Lock()
	defer rm.registeredResourceMapLock.Unlock()

	if _, ok := rm.registeredResourceMap[resourceID]; ok {
		err := ErrorResourceAlreadyInit
		return err
	}

	r := &resource{
		resourceID: resourceID,
		user:       user,
		param:      param,
		status:     resourceStatusInit,
		brokerName: param.BrokerName,
		initTime:   time.Now().Local(),
	}
	if err := rm.createResources(r); err != nil {
		blog.Errorf("crm: create resource(%s) failed: %v", resourceID, err)

		return err
	}

	rm.registeredResourceMap[resourceID] = r
	return nil
}

func (rm *resourceManager) listResources(status ...resourceStatus) ([]*resource, error) {
	opts := commonMySQL.NewListOptions()
	opts.In("status", status)
	opts.Limit(-1)
	trl, _, err := rm.mysql.ListResource(opts)
	if err != nil {
		blog.Errorf("crm: list resources with status(%v) failed: %v", status, err)
		return nil, err
	}

	rl := make([]*resource, 0, 500)
	for _, tr := range trl {
		rl = append(rl, table2Resource(tr))
	}

	return rl, nil
}

func (rm *resourceManager) createResources(r *resource) error {
	return rm.mysql.CreateResource(resource2Table(r))
}

func (rm *resourceManager) saveResources(r *resource) error {
	rm.registeredResourceMapLock.Lock()
	defer rm.registeredResourceMapLock.Unlock()

	rm.registeredResourceMap[r.resourceID] = r
	if r.status == resourceStatusReleased {
		delete(rm.registeredResourceMap, r.resourceID)
	}

	return rm.mysql.PutResource(resource2Table(r))
}

func (rm *resourceManager) getResources(resourceID string) (*resource, error) {
	rm.registeredResourceMapLock.Lock()
	defer rm.registeredResourceMapLock.Unlock()

	r, ok := rm.registeredResourceMap[resourceID]
	if !ok {
		return nil, ErrorResourceNoExist
	}

	return copyResource(r), nil
}

func (rm *resourceManager) getServiceInfo(resourceID, user string) (*op.ServiceInfo, error) {
	if !rm.running {
		return nil, ErrorManagerNotRunning
	}

	targetID, err := rm.getServerRealName(resourceID)
	if err != nil {
		blog.Errorf("crm: get service info for resource(%s) user(%s), get server real name failed: %v",
			resourceID, user, err)
		return nil, err
	}

	info, err := rm.operator.GetServerStatus(rm.conf.BcsClusterID, user, targetID)
	if err != nil {
		blog.Errorf("crm: get service info for resource(%s) target(%s) user(%s) failed: %v",
			resourceID, targetID, user, err)
		return nil, err
	}

	terminated := false
	switch info.Status {
	//ServiceStatusRunning means all resource ready
	case ServiceStatusRunning, ServiceStatusFailed:
		terminated = true
	}

	rm.freshDeployingStatus(resourceID, user, info.CurrentInstances, terminated)
	return info, nil
}

func (rm *resourceManager) isServicePreparing(resourceID, user string) (bool, error) {
	if !rm.running {
		return false, ErrorManagerNotRunning
	}

	r, err := rm.getResources(resourceID)
	// if resource not exist, it's meet "no preparing"
	if err == ErrorResourceNoExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	switch r.status {
	case resourceStatusInit, resourceStatusDeploying:
		return true, nil
	default:
		return false, nil
	}
}

func (rm *resourceManager) getServerRealName(resourceID string) (string, error) {
	rm.lockResource(resourceID)
	defer rm.unlockResource(resourceID)

	r, err := rm.getResources(resourceID)
	if err != nil {
		blog.Errorf("crm: get server real name for resource(%s) failed: %v", resourceID, err)
		return "", err
	}

	if r.brokerResourceID != "" {
		return r.brokerResourceID, nil
	}

	return resourceID, nil
}

func (rm *resourceManager) getFreeInstances(
	condition map[string]string,
	function op.InstanceFilterFunction) (int, string, error) {

	return rm.nodeInfoPool.GetFreeInstances(condition, function)
}

func (rm *resourceManager) releaseNoReadyInstance(key string, instance int) {
	now := time.Now()
	for ; ; time.Sleep(syncTimeGap) {
		if rm.nodeInfoPool.GetLastUpdateTime().After(now) {
			rm.nodeInfoPool.ReleaseNoReadyInstance(key, instance)
			break
		}
	}
}

func (rm *resourceManager) launch(
	resourceID, user, city string,
	function op.InstanceFilterFunction,
	useBroker bool) error {

	if !rm.running {
		return ErrorManagerNotRunning
	}

	hasBroker := false
	needTrace := false

	rm.lockResource(resourceID)
	defer func() {
		rm.unlockResource(resourceID)
		if needTrace {
			if !hasBroker || !rm.isFinishDeploying(resourceID, user) {
				// begin to trace resource until it finish deploying
				go rm.trace(resourceID, user)
			}
		}
	}()

	r, err := rm.getResources(resourceID)
	if err != nil {
		blog.Errorf("crm: try launching service, get resource(%s) for user(%s) failed: %v",
			resourceID, user, err)
		return err
	}

	if r.status != resourceStatusInit {
		err = ErrorApplicationAlreadyLaunched
		blog.Errorf("crm: try launching service failed, resource(%s) user(%s): %v", resourceID, user, err)
		return err
	}

	// specify city
	originCity := r.param.City
	if city != "" {
		r.param.City = city
	}

	// try apply resource from brokers
	if rm.brokerSet != nil && useBroker {
		if brokerID, err := rm.brokerSet.Apply(resourceID, user, r.param, function); err == nil {
			hasBroker = true
			r.brokerResourceID = brokerID
			blog.Infof("crm: success to apply resource(%s) with broker(%s)", resourceID, brokerID)
		}
	}

	if !hasBroker && useBroker && rm.conf.Operator == config.CRMOperatorDCMac {
		blog.Warnf("crm: failed to apply resource(%s) for operator %s from broker", resourceID, rm.conf.Operator)
		return ErrorBrokerNotEnoughResources
	}

	if !hasBroker {
		condition := map[string]string{
			op.AttributeKeyCity:     r.param.City,
			op.AttributeKeyPlatform: r.param.Platform,
		}
		instance, key, err := rm.getFreeInstances(condition, function)
		if err == engine.ErrorNoEnoughResources || err == ErrorBrokerNotEnoughResources {
			return err
		}
		if err != nil {
			blog.Errorf("crm: try get free instances for resource(%s) user(%s) failed: %v",
				resourceID, user, err)
			return err
		}

		r.noReadyInstance = instance
		r.resourceBlockKey = key

		blog.Infof("crm: try to launch service with resource(%s) instance(%d) for user(%s)",
			resourceID, instance, user)
		if err = rm.operator.LaunchServer(rm.conf.BcsClusterID, op.BcsLaunchParam{
			Name:               resourceID,
			Namespace:          user,
			AttributeCondition: condition,
			Env:                r.param.Env,
			Ports:              r.param.Ports,
			Volumes:            r.param.Volumes,
			Image:              r.param.Image,
			Instance:           instance,
		}); err != nil {
			blog.Errorf("crm: launch service with resource(%s) for user(%s) failed: %v", resourceID, user, err)

			// if launch failed, clean the dirty data in noReadyInstance
			go rm.releaseNoReadyInstance(r.resourceBlockKey, r.noReadyInstance)
			return err
		}

		r.requestInstance = instance
	}

	r.status = resourceStatusDeploying
	if err = rm.saveResources(r); err != nil {
		blog.Errorf("crm: try launching service, save resource(%s) for user(%s) failed: %v",
			resourceID, user, err)

		// if save resource failed, clean the dirty data in noReadyInstance
		go rm.releaseNoReadyInstance(r.resourceBlockKey, r.noReadyInstance)
		return err
	}

	needTrace = true

	blog.Infof(
		"crm: success to launch service with resource(%s) instance(%d) "+
			"for user(%s) in city(%s) from originCity(%s)",
		resourceID, r.requestInstance, user, r.param.City, originCity,
	)
	return nil
}

func (rm *resourceManager) scale(resourceID, user string, function op.InstanceFilterFunction) error {
	if !rm.running {
		return ErrorManagerNotRunning
	}

	rm.lockResource(resourceID)
	defer rm.unlockResource(resourceID)

	r, err := rm.getResources(resourceID)
	if err != nil {
		blog.Errorf("crm: try scaling service, get resource(%s) for user(%s) failed: %v", resourceID, user, err)
		return err
	}

	if r.status != resourceStatusRunning {
		err = ErrorResourceNotRunning
		blog.Errorf("crm: try scaling service failed, resource(%s) user(%s) status(%s) failed: %v",
			resourceID, user, r.status, err)
		return err
	}

	hasBroker := false
	if r.brokerResourceID != "" {
		hasBroker = true
		if err = rm.scale(r.brokerResourceID, user, function); err != nil {
			blog.Errorf("crm: try scaling resource(%s) broker(%s) user(%s) failed: %v",
				resourceID, r.brokerResourceID, user, err)
			return err
		}
	}

	if !hasBroker {
		condition := map[string]string{
			op.AttributeKeyCity: r.param.City,
		}
		deltaInstance, key, err := rm.getFreeInstances(condition, function)
		if err != nil {
			blog.Errorf("crm: try get free instances for resource(%s) user(%s) failed: %v",
				resourceID, user, err)
			return err
		}

		targetInstance := r.requestInstance + deltaInstance
		if deltaInstance > 0 {
			r.noReadyInstance = deltaInstance
			r.resourceBlockKey = key
		}

		blog.Infof("crm: try to scale service with resource(%s) instance(%d->%d) for user(%s)",
			resourceID, r.requestInstance, targetInstance, user)
		if err = rm.operator.ScaleServer(rm.conf.BcsClusterID, user, resourceID, targetInstance); err != nil {
			blog.Errorf("crm: scale service with resource(%s) instance(%d->%d) for user(%s) failed: %v",
				resourceID, r.requestInstance, targetInstance, user, err)

			// if scale failed, clean the dirty data in noReadyInstance
			go rm.releaseNoReadyInstance(r.resourceBlockKey, r.noReadyInstance)
			return err
		}

		r.requestInstance = targetInstance
	}

	r.status = resourceStatusDeploying
	if err = rm.saveResources(r); err != nil {
		blog.Errorf("crm: try scaling service, save resource(%s) for user(%s) failed: %v", resourceID, user, err)

		// if save resource failed, clean the dirty data in noReadyInstance
		go rm.releaseNoReadyInstance(r.resourceBlockKey, r.noReadyInstance)
		return err
	}

	// begin to trace resource until it finish deploying
	go rm.trace(resourceID, user)
	blog.Infof("crm: success to scale service with resource(%s) for user(%s)", resourceID, user)
	return nil
}

func (rm *resourceManager) release(resourceID, user string) error {
	if !rm.running {
		return ErrorManagerNotRunning
	}

	rm.lockResource(resourceID)
	defer rm.unlockResource(resourceID)

	r, err := rm.getResources(resourceID)
	if err != nil {
		blog.Errorf("crm: try releasing service, get resource(%s) for user(%s) failed: %v",
			resourceID, user, err)
		return err
	}

	if r.status == resourceStatusReleased {
		err = ErrorResourceAlreadyReleased
		blog.Errorf("crm: try releasing service failed, resource(%s) user(%s): %v", resourceID, user, err)
		return err
	}

	if r.brokerResourceID != "" {
		blog.Infof("crm: resource(%s) user(%s) has broker(%s), release broker first",
			resourceID, user, r.brokerResourceID)
		if err = rm.release(r.brokerResourceID, user); err != nil {
			blog.Errorf("crm: try to release resource(%s) user(%s)'s broker(%s) failed: %v",
				resourceID, user, r.brokerResourceID, err)
			return err
		}
	} else {
		if err = rm.operator.ReleaseServer(rm.conf.BcsClusterID, user, resourceID); err != nil {
			blog.Errorf("crm: release service with resource(%s) for user(%s) failed: %v",
				resourceID, user, err)
			return err
		}
	}

	if r.noReadyInstance > 0 {
		go rm.releaseNoReadyInstance(r.resourceBlockKey, r.noReadyInstance)
		r.noReadyInstance = 0
	}
	r.status = resourceStatusReleased
	if err = rm.saveResources(r); err != nil {
		blog.Errorf("crm: try releasing service, save resource(%s) for user(%s) failed: %v",
			resourceID, user, err)
		return err
	}

	blog.Infof("crm: success to release resource(%s)", resourceID)
	return nil
}

func (rm *resourceManager) addBroker(
	name, user string,
	strategyType StrategyType,
	strategy BrokerStrategy,
	param BrokerParam) error {

	broker := NewBroker(name, user, rm, strategyType, strategy, param)
	if rm.running {
		if err := broker.Run(); err != nil {
			blog.Errorf("crm: add broker(%s) with user(%s) param(%+v) failed: %v", name, user, param, err)
			return err
		}
	}
	rm.brokerSet.Add(broker)
	return nil
}

func (rm *resourceManager) strategyConst(broker *Broker) int {
	return broker.strategy.Ask(broker.CurrentNum())
}

func (rm *resourceManager) checkBroker(broker *Broker) {
	delta := 0
	switch broker.strategyType {
	case StrategyConst:
		delta = rm.strategyConst(broker)
	}

	if delta == 0 {
		return
	}

	blog.Infof("crm: try to launch %d resource for broker(%s) user(%s)", delta, broker.name, broker.user)
	if delta > 0 {
		for i := 0; i < delta; i++ {
			if err := broker.Launch(); err != nil {
				switch err {
				case ErrorBrokerNotEnoughResources, ErrorBrokeringUnderCoolingTime:
					blog.Errorf("crm: try launching resource for broker(%s) with user(%s) failed: %v",
						broker.name, broker.user, err)
					return
				}
				blog.Errorf("crm: try launching resource for broker(%s) with user(%s) failed: %v",
					broker.name, broker.user, err)
				return
			}
		}
		return
	}

	blog.Infof("crm: try to release %d resource for broker(%s) user(%s)", -delta, broker.name, broker.user)
	for i := 0; i < (-delta); i++ {
		if err := broker.Release(); err != nil {
			blog.Errorf("crm: try releasing resource for broker(%s) with user(%s) failed: %v",
				broker.name, broker.user, err)
			return
		}
	}

	return
}

func (rm *resourceManager) checkBrokers() {
	for _, broker := range rm.brokerSet.List() {
		rm.checkBroker(broker)
	}
}

func (rm *resourceManager) runBrokerChecker() {
	time.Sleep(brokerCheckerSleepTime)
	ticker := time.NewTicker(brokerCheckerTimeGap)
	defer ticker.Stop()

	blog.Infof("crm: start broker checker")

	for {
		select {
		case <-rm.ctx.Done():
			blog.Warnf("crm broker: broker checking done")
			return
		case <-ticker.C:
			rm.checkBrokers()
		}
	}
}

type handlerWithUser struct {
	user string
	mgr  *resourceManager
}

// Init
func (hwu *handlerWithUser) Init(resourceID string, param ResourceParam) error {
	return hwu.mgr.init(hwu.resourceID(resourceID), hwu.user, param)
}

// Launch
func (hwu *handlerWithUser) Launch(resourceID, city string, function op.InstanceFilterFunction) error {
	return hwu.mgr.launch(hwu.resourceID(resourceID), hwu.user, city, function, true)
}

// Scale
func (hwu *handlerWithUser) Scale(resourceID string, function op.InstanceFilterFunction) error {
	return hwu.mgr.scale(hwu.resourceID(resourceID), hwu.user, function)
}

// GetServiceInfo
func (hwu *handlerWithUser) GetServiceInfo(resourceID string) (*op.ServiceInfo, error) {
	return hwu.mgr.getServiceInfo(hwu.resourceID(resourceID), hwu.user)
}

// IsServicePreparing
func (hwu *handlerWithUser) IsServicePreparing(resourceID string) (bool, error) {
	return hwu.mgr.isServicePreparing(hwu.resourceID(resourceID), hwu.user)
}

// Release
func (hwu *handlerWithUser) Release(resourceID string) error {
	return hwu.mgr.release(hwu.resourceID(resourceID), hwu.user)
}

// AddBroker add a broker settings into handler
func (hwu *handlerWithUser) AddBroker(
	name string,
	strategyType StrategyType,
	strategy BrokerStrategy,
	param BrokerParam) error {

	return hwu.mgr.addBroker(name, hwu.user, strategyType, strategy, param)
}

//ResourceManager return the rm
func (hwu *handlerWithUser) GetInstanceType(platform string, group string) *config.InstanceType {
	retIst := config.InstanceType{
		CPUPerInstance: hwu.mgr.conf.BcsCPUPerInstance,
		MemPerInstance: hwu.mgr.conf.BcsMemPerInstance,
	}
	for _, istItem := range hwu.mgr.conf.InstanceType {
		if !(istItem.Group == group && istItem.Platform == platform) {
			continue
		}
		if istItem.CPUPerInstance > 0.0 {
			retIst.CPUPerInstance = istItem.CPUPerInstance
		}
		if istItem.MemPerInstance > 0.0 {
			retIst.MemPerInstance = istItem.MemPerInstance
		}
		break
	}
	return &retIst
}

func (hwu *handlerWithUser) resourceID(id string) string {
	return strings.ReplaceAll(strings.ToLower(fmt.Sprintf("%s-%s", hwu.user, id)), "_", "-")
}

type resource struct {
	resourceID string
	user       string
	param      ResourceParam

	resourceBlockKey string
	noReadyInstance  int
	requestInstance  int

	status resourceStatus

	// if this resource is a normal resource, and it links to a broker resource,
	// then the brokerResourceID is the resourceID of the broker resource, or it is empty
	brokerResourceID string

	// if this resource is a broker resource,
	// then the brokerName is the name of this broker, or it is empty
	brokerName string

	// if this resource is a broker resource, and it is sold to another normal resource,
	// then the brokerSold is true
	brokerSold bool

	initTime time.Time
}

type resourceStatus int

// String return the string of resourceStatus
func (rs resourceStatus) String() string {
	return resourceStatusMap[rs]
}

const (
	resourceStatusInit resourceStatus = iota
	resourceStatusDeploying
	resourceStatusRunning
	resourceStatusReleased
	resourceStatusDeleting
)

var resourceStatusMap = map[resourceStatus]string{
	resourceStatusInit:      "init",
	resourceStatusDeploying: "deploying",
	resourceStatusRunning:   "running",
	resourceStatusReleased:  "released",
	resourceStatusDeleting:  "deleting",
}

func table2Resource(tr *TableResource) *resource {
	var param ResourceParam
	_ = codec.DecJSON([]byte(tr.Param), &param)

	return &resource{
		resourceID:       tr.ResourceID,
		user:             tr.User,
		param:            param,
		resourceBlockKey: tr.ResourceBlockKey,
		noReadyInstance:  tr.NoReadyInstance,
		requestInstance:  tr.RequestInstance,
		status:           resourceStatus(tr.Status),
		brokerResourceID: tr.BrokerResourceID,
		brokerName:       tr.BrokerName,
		brokerSold:       tr.BrokerSold,

		// TODO: 应该将时间节点存入db, 当前先用从db获取的时间.
		initTime: time.Now().Local(),
	}
}

func resource2Table(r *resource) *TableResource {
	var param []byte
	_ = codec.EncJSON(r.param, &param)

	return &TableResource{
		ResourceID:       r.resourceID,
		User:             r.user,
		Param:            string(param),
		ResourceBlockKey: r.resourceBlockKey,
		NoReadyInstance:  r.noReadyInstance,
		RequestInstance:  r.requestInstance,
		Status:           int(r.status),
		BrokerResourceID: r.brokerResourceID,
		BrokerName:       r.brokerName,
		BrokerSold:       r.brokerSold,
	}
}

func copyResource(res *resource) *resource {
	r := new(resource)
	*r = *res
	return r
}
