// Utils provide a series of helper functions.

package cache

import (
	"bytes"
	"net/http"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/gophercloud/utils/openstack/clientconfig"
	"github.com/openstack-exporter/openstack-exporter/exporters"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
)

// CollectCache collects the MetricsFamily for required clouds and services and stores in the cache.
func CollectCache(
	enableExporterFunc func(string, string, string, []string, string, bool, bool, bool, bool, string, func() (string, error), log.Logger) (*exporters.OpenStackExporter, error), multiCloud bool, services map[string]*bool, prefix, cloud string, disabledMetrics []string, endpointType string, collectTime bool, disableSlowMetrics bool, disableDeprecatedMetrics bool, disableCinderAgentUUID bool, domainID string, uuidGenFunc func() (string, error), logger log.Logger) error {
	level.Info(logger).Log("msg", "Run collect cache job")
	cacheBackend := GetCache()

	clouds := []string{}

	if multiCloud {
		cloudsConfig, err := clientconfig.LoadCloudsYAML()
		if err != nil {
			return err
		}
		for cloud := range cloudsConfig {
			clouds = append(clouds, cloud)
		}
	}
	if cloud != "" && !multiCloud {
		clouds = append(clouds, cloud)
	}

	enabledServices := []string{}
	for service, disabled := range services {
		if !*disabled {
			enabledServices = append(enabledServices, service)
		}
	}

	for _, cloud := range clouds {
		registry := prometheus.NewPedanticRegistry()
		for _, service := range enabledServices {
			level.Info(logger).Log("msg", "Start update cache data", "cloud", cloud, "service", service)
			exp, err := enableExporterFunc(service, prefix, cloud, disabledMetrics, endpointType, collectTime, disableSlowMetrics, disableDeprecatedMetrics, disableCinderAgentUUID, domainID, nil, logger)
			if err != nil {
				// Log error and continue with enabling other exporters
				level.Error(logger).Log("err", "enabling exporter for service failed", "cloud", cloud, "service", service, "error", err)
				continue
			}
			registry.MustRegister(*exp)

			metricFamilies, err := registry.Gather()
			if err != nil {
				level.Error(logger).Log("err", "Create gather failed", "cloud", cloud, "service", service, "error", err)
				continue
			}
			for _, mf := range metricFamilies {
				cacheBackend.SetMetricFamilyCache(
					cloud, service, *mf.Name, MetricFamilyCache{
						MF: mf,
					},
				)
				level.Debug(logger).Log("msg", "Update cache data", "cloud", cloud, "service", service, "MetricsFamily", mf.Name)
			}
			level.Info(logger).Log("msg", "Finish update cache data", "cloud", cloud, "service", service)
		}
	}

	return nil
}

// BufferFromCache reads cloud's MetricsFamily data from cache and writes into a buffer.
func BufferFromCache(cloud string, services []string, logger log.Logger) (bytes.Buffer, error) {
	cacheBackend := GetCache()
	var buf bytes.Buffer
	for _, service := range services {
		serviceCacheData, exists := cacheBackend.GetServiceCache(cloud, service)
		if exists {
			for name, mfCache := range serviceCacheData.MetricFamilyCaches {
				level.Debug(logger).Log("msg", "Get metric from cache", "cloud", cloud, "service", service, "name", name)
				_, err := expfmt.MetricFamilyToText(&buf, mfCache.MF)
				if err != nil {
					return buf, err
				}
			}
		} else {
			level.Debug(logger).Log("msg", "Missing service cache", "cloud", cloud, "service", service)
		}
	}
	return buf, nil
}

// FlushExpiredCloudCaches flush expired caches based on cloud's update time
func FlushExpiredCloudCaches(ttl time.Duration) {
	cacheBackend := GetCache()
	cacheBackend.FlushExpiredCloudCaches(ttl)
}

// WriteCacheToResponse read cache and write to the connection as part of an HTTP reply.
func WriteCacheToResponse(w http.ResponseWriter, r *http.Request, cloud string, enabledServices []string, logger log.Logger) error {
	buf, err := BufferFromCache(cloud, enabledServices, logger)
	if err != nil {
		http.Error(w, "Failed to encode metrics", http.StatusInternalServerError)
	}
	opts := promhttp.HandlerOpts{}

	// Follow the way how promehttp package set up the contentType
	var contentType expfmt.Format
	if opts.EnableOpenMetrics {
		contentType = expfmt.NegotiateIncludingOpenMetrics(r.Header)
	} else {
		contentType = expfmt.Negotiate(r.Header)
	}
	w.Header().Set("Context-Type", string(contentType))
	if _, err = w.Write(buf.Bytes()); err != nil {
		http.Error(w, "Failed to write cached metrics to response", http.StatusInternalServerError)
	}
	return nil
}
