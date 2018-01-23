// RAINBOND, Application Management Platform
// Copyright (C) 2014-2017 Goodrain Co., Ltd.

// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version. For any non-GPL usage of Rainbond,
// one or multiple Commercial Licenses authorized by Goodrain Co., Ltd.
// must be obtained first.

// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package collector

import (
	"fmt"
	"os"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/goodrain/rainbond/pkg/db"
	"github.com/goodrain/rainbond/pkg/db/model"
	"github.com/goodrain/rainbond/pkg/status"
	"github.com/goodrain/rainbond/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
)

//Exporter 收集器
type Exporter struct {
	dsn           string
	error         prometheus.Gauge
	totalScrapes  prometheus.Counter
	scrapeErrors  *prometheus.CounterVec
	memoryUse     *prometheus.GaugeVec
	fsUse         *prometheus.GaugeVec
	workerUp      prometheus.Gauge
	dbmanager     db.Manager
	statusManager status.ServiceStatusManager
}

var scrapeDurationDesc = prometheus.NewDesc(
	prometheus.BuildFQName(namespace, "exporter", "collector_duration_seconds"),
	"Collector time duration.",
	[]string{"collector"}, nil,
)

//Describe Describe
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	metricCh := make(chan prometheus.Metric)
	doneCh := make(chan struct{})

	go func() {
		for m := range metricCh {
			ch <- m.Desc()
		}
		close(doneCh)
	}()

	e.Collect(metricCh)
	close(metricCh)
	<-doneCh
}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)

	ch <- e.totalScrapes
	ch <- e.error
	e.fsUse.Collect(ch)
	e.memoryUse.Collect(ch)
	e.scrapeErrors.Collect(ch)
	ch <- e.workerUp
}

func (e *Exporter) scrape(ch chan<- prometheus.Metric) {
	e.totalScrapes.Inc()
	var err error
	scrapeTime := time.Now()
	services, err := e.dbmanager.TenantServiceDao().GetAllServices()
	if err != nil {
		logrus.Errorln("Error scraping for tenant service when select db :", err)
		e.scrapeErrors.WithLabelValues("db.getservices").Inc()
		e.error.Set(1)
	}
	volumes, err := e.dbmanager.TenantServiceVolumeDao().GetAllVolumes()
	if err != nil {
		logrus.Errorln("Error scraping for tenant service when select db :", err)
		e.scrapeErrors.WithLabelValues("db.getvolumes").Inc()
		e.error.Set(1)
	}
	localPath := os.Getenv("LOCAL_DATA_PATH")
	sharePath := os.Getenv("SHARE_DATA_PATH")
	if localPath == "" {
		localPath = "/grlocaldata"
	}
	if sharePath == "" {
		sharePath = "/grdata"
	}
	//获取内存使用情况
	var cache = make(map[string]*model.TenantServices)
	for _, service := range services {
		if appstatus, err := e.statusManager.GetStatus(service.ServiceID); err == nil {
			if appstatus != status.CLOSED && appstatus != status.UNDEPLOY && appstatus != status.DEPLOYING {
				e.memoryUse.WithLabelValues(service.TenantID, service.ServiceAlias, appstatus).Set(float64(service.ContainerMemory * service.Replicas))
			}
		}
		//默认目录
		size := util.GetDirSize(fmt.Sprintf("%s/tenant/%s/service/%s", sharePath, service.TenantID, service.ServiceID))
		if size != 0 {
			e.fsUse.WithLabelValues(service.TenantID, service.ServiceID, string(model.ShareFileVolumeType)).Set(size)
		}
		cache[service.ServiceID] = service
	}
	ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, time.Since(scrapeTime).Seconds(), "collect.memory")
	scrapeTime = time.Now()
	gettenantID := func(serviceID string) string {
		if service, ok := cache[serviceID]; ok {
			return service.TenantID
		}
		return ""
	}
	//获取磁盘使用情况
	for _, volume := range volumes {
		var size float64
		if volume.VolumeType == string(model.LocalVolumeType) {
			size = util.GetDirSize(volume.HostPath)
			if size != 0 {
				e.fsUse.WithLabelValues(gettenantID(volume.ServiceID), volume.ServiceID, volume.VolumeType).Set(size)
			}
		}
	}
	ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, time.Since(scrapeTime).Seconds(), "collect.fs")
}

var namespace = "app_resource"

//New 创建一个收集器
func New(statusManager status.ServiceStatusManager) *Exporter {
	return &Exporter{
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "exporter",
			Name:      "scrapes_total",
			Help:      "Total number of times Worker was scraped for metrics.",
		}),
		scrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: "exporter",
			Name:      "scrape_errors_total",
			Help:      "Total number of times an error occurred scraping a Worker.",
		}, []string{"collector"}),
		error: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: "exporter",
			Name:      "last_scrape_error",
			Help:      "Whether the last scrape of metrics from Worker resulted in an error (1 for error, 0 for success).",
		}),
		workerUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "Whether the Worker server is up.",
		}),
		memoryUse: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "appmemory",
			Help:      "tenant service memory used.",
		}, []string{"tenant_id", "service_id", "service_status"}),
		fsUse: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "appfs",
			Help:      "tenant service fs used.",
		}, []string{"tenant_id", "service_id", "volume_type"}),
		dbmanager:     db.GetManager(),
		statusManager: statusManager,
	}
}
