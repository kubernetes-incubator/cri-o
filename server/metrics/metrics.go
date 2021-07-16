package metrics

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	libconfig "github.com/cri-o/cri-o/pkg/config"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/util/cert"
)

const (
	// CRIOOperationsKey is the key for CRI-O operation metrics.
	CRIOOperationsKey = "crio_operations"

	// CRIOOperationsLatencyTotalKey is the key for the operation latency metrics.
	CRIOOperationsLatencyTotalKey = "crio_operations_latency_microseconds_total"

	// CRIOOperationsLatencyKey is the key for the operation latency metrics for each CRI call.
	CRIOOperationsLatencyKey = "crio_operations_latency_microseconds"

	// CRIOOperationsErrorsKey is the key for the operation error metrics.
	CRIOOperationsErrorsKey = "crio_operations_errors"

	// CRIOImagePullsByDigestKey is the key for CRI-O image pull metrics by digest.
	CRIOImagePullsByDigestKey = "crio_image_pulls_by_digest"

	// CRIOImagePullsByNameKey is the key for CRI-O image pull metrics by name.
	CRIOImagePullsByNameKey = "crio_image_pulls_by_name"

	// CRIOImagePullsByNameSkippedKey is the key for CRI-O skipped image pull metrics by name (skipped).
	CRIOImagePullsByNameSkippedKey = "crio_image_pulls_by_name_skipped"

	// CRIOImagePullsFailuresKey is the key for failed image downloads in CRI-O.
	CRIOImagePullsFailuresKey = "crio_image_pulls_failures"

	// CRIOImagePullsSuccessesKey is the key for successful image downloads in CRI-O.
	CRIOImagePullsSuccessesKey = "crio_image_pulls_successes"

	// CRIOImagePullsLayerSize is the key for CRI-O image pull metrics per layer.
	CRIOImagePullsLayerSize = "crio_image_pulls_layer_size"

	// CRIOImageLayerReuseKey is the key for the CRI-O image layer reuse metrics.
	CRIOImageLayerReuseKey = "crio_image_layer_reuse"

	// CRIOContainersOOMTotalKey is the key for the total CRI-O container out of memory metrics.
	CRIOContainersOOMTotalKey = "crio_containers_oom_total"

	// CRIOContainersOOMKey is the key for the CRI-O container out of memory metrics per container name.
	CRIOContainersOOMKey = "crio_containers_oom"

	subsystem = "container_runtime"
)

// SinceInMicroseconds gets the time since the specified start in microseconds.
func SinceInMicroseconds(start time.Time) float64 {
	return float64(time.Since(start).Microseconds())
}

// Metrics is the main structure for starting the metrics endpoints.
type Metrics struct {
	impl                          Impl
	finished                      chan bool
	config                        *libconfig.MetricsConfig
	metricOperations              *prometheus.CounterVec
	metricOperationsLatency       *prometheus.GaugeVec
	metricOperationsLatencyTotal  *prometheus.SummaryVec
	metricOperationsErrors        *prometheus.CounterVec
	metricImagePullsByDigest      *prometheus.CounterVec
	metricImagePullsByName        *prometheus.CounterVec
	metricImagePullsByNameSkipped *prometheus.CounterVec
	metricImagePullsFailures      *prometheus.CounterVec
	metricImagePullsSuccesses     *prometheus.CounterVec
	metricImagePullsLayerSize     prometheus.Histogram
	metricImageLayerReuse         *prometheus.CounterVec
	metricContainersOOMTotal      prometheus.Counter
	metricContainersOOM           *prometheus.CounterVec
}

var instance *Metrics

// New creates a new metrics instance.
func New(config *libconfig.MetricsConfig) *Metrics {
	instance = &Metrics{
		impl:     &defaultImpl{},
		finished: make(chan bool),
		config:   config,
		metricOperations: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOOperationsKey,
				Help:      "Cumulative number of CRI-O operations by operation type.",
			},
			[]string{"operation_type"},
		),
		metricOperationsLatency: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Subsystem: subsystem,
				Name:      CRIOOperationsLatencyKey,
				Help:      "Latency in microseconds of individual CRI calls for CRI-O operations. Broken down by operation type.",
			},
			[]string{"operation_type"},
		),
		metricOperationsLatencyTotal: prometheus.NewSummaryVec(
			prometheus.SummaryOpts{
				Subsystem: subsystem,
				Name:      CRIOOperationsLatencyTotalKey,
				Help:      "Latency in microseconds of CRI-O operations. Broken down by operation type.",
			},
			[]string{"operation_type"},
		),
		metricOperationsErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOOperationsErrorsKey,
				Help:      "Cumulative number of CRI-O operation errors by operation type.",
			},
			[]string{"operation_type"},
		),
		metricImagePullsByDigest: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOImagePullsByDigestKey,
				Help:      "Bytes transferred by CRI-O image pulls by digest",
			},
			[]string{"name", "digest", "mediatype", "size"},
		),
		metricImagePullsByName: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOImagePullsByNameKey,
				Help:      "Bytes transferred by CRI-O image pulls by name",
			},
			[]string{"name", "size"},
		),
		metricImagePullsByNameSkipped: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOImagePullsByNameSkippedKey,
				Help:      "Bytes skipped by CRI-O image pulls by name",
			},
			[]string{"name"},
		),
		metricImagePullsFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOImagePullsFailuresKey,
				Help:      "Cumulative number of CRI-O image pull failures by error.",
			},
			[]string{"name", "error"},
		),
		metricImagePullsSuccesses: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOImagePullsSuccessesKey,
				Help:      "Cumulative number of CRI-O image pull successes.",
			},
			[]string{"name"},
		),
		metricImagePullsLayerSize: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Subsystem: subsystem,
				Name:      CRIOImagePullsLayerSize,
				Help:      "Bytes transferred by CRI-O image pulls per layer",
				Buckets: []float64{ // in bytes
					1000,                    //   1 KiB
					1000 * 1000,             //   1 MiB
					10 * 1000 * 1000,        //  10 MiB
					50 * 1000 * 1000,        //  50 MiB
					100 * 1000 * 1000,       // 100 MiB
					200 * 1000 * 1000,       // 200 MiB
					300 * 1000 * 1000,       // 300 MiB
					400 * 1000 * 1000,       // 400 MiB
					500 * 1000 * 1000,       // 500 MiB
					1000 * 1000 * 1000,      //   1 GiB
					10 * 1000 * 1000 * 1000, //  10 GiB
				},
			},
		),
		metricImageLayerReuse: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOImageLayerReuseKey,
				Help:      "Reused (not pulled) local image layer count by name",
			},
			[]string{"name"},
		),
		metricContainersOOMTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOContainersOOMTotalKey,
				Help:      "Amount of containers killed because they ran out of memory (OOM)",
			},
		),
		metricContainersOOM: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Subsystem: subsystem,
				Name:      CRIOContainersOOMKey,
				Help:      "Amount of containers killed because they ran out of memory (OOM) by their name",
			},
			[]string{"name"},
		),
	}
	return Instance()
}

// Instance returns the singleton instance of the Metrics.
func Instance() *Metrics {
	if instance == nil {
		return New(&libconfig.MetricsConfig{})
	}
	return instance
}

// Start starts serving the metrics in the background.
func (m *Metrics) Start(stop chan struct{}) error {
	if m.config == nil {
		return errors.New("provided config is nil")
	}

	me, err := m.createEndpoint()
	if err != nil {
		return errors.Wrap(err, "create endpoint")
	}

	if err := m.startEndpoint(
		stop, "tcp", fmt.Sprintf(":%v", m.config.MetricsPort), me,
	); err != nil {
		return errors.Wrapf(
			err, "create metrics endpoint on port %d", m.config.MetricsPort,
		)
	}

	metricsSocket := m.config.MetricsSocket
	if metricsSocket != "" {
		if err := m.impl.RemoveUnusedSocket(metricsSocket); err != nil {
			return errors.Wrapf(err, "removing unused socket %s", metricsSocket)
		}

		return errors.Wrap(
			m.startEndpoint(stop, "unix", m.config.MetricsSocket, me),
			"creating metrics endpoint socket",
		)
	}

	return nil
}

func (m *Metrics) MetricOperationsInc(operation string) {
	c, err := m.metricOperations.GetMetricWithLabelValues(operation)
	if err != nil {
		logrus.Warnf("Unable to write operations metric: %v", err)
		return
	}
	c.Inc()
}

func (m *Metrics) MetricOperationsLatencySet(operation string, start time.Time) {
	g, err := m.metricOperationsLatency.GetMetricWithLabelValues(operation)
	if err != nil {
		logrus.Warnf("Unable to write operation latency metric: %v", err)
		return
	}
	g.Set(SinceInMicroseconds(start))
}

func (m *Metrics) MetricOperationsLatencyTotalObserve(operation string, start time.Time) {
	o, err := m.metricOperationsLatencyTotal.GetMetricWithLabelValues(operation)
	if err != nil {
		logrus.Warnf("Unable to write operation latency (total) metric: %v", err)
		return
	}
	o.Observe(SinceInMicroseconds(start))
}

func (m *Metrics) MetricOperationsErrorsInc(operation string) {
	c, err := m.metricOperationsErrors.GetMetricWithLabelValues(operation)
	if err != nil {
		logrus.Warnf("Unable to write operation errors metric: %v", err)
		return
	}
	c.Inc()
}

func (m *Metrics) MetricContainersOOMInc(name string) {
	c, err := m.metricContainersOOM.GetMetricWithLabelValues(name)
	if err != nil {
		logrus.Warnf("Unable to write container OOM metric: %v", err)
		return
	}
	c.Inc()
}

func (m *Metrics) MetricContainersOOMTotalInc() {
	m.metricContainersOOMTotal.Inc()
}

func (m *Metrics) MetricImagePullsLayerSizeObserve(size int64) {
	m.metricImagePullsLayerSize.Observe(float64(size))
}

func (m *Metrics) MetricImagePullsByNameSkippedAdd(add float64, name string) {
	c, err := m.metricImagePullsByNameSkipped.GetMetricWithLabelValues(name)
	if err != nil {
		logrus.Warnf("Unable to write image pulls by name skipped metric: %v", err)
		return
	}
	c.Add(add)
}

func (m *Metrics) MetricImagePullsFailuresInc(image, label string) {
	c, err := m.metricImagePullsFailures.GetMetricWithLabelValues(image, label)
	if err != nil {
		logrus.Warnf("Unable to write image pull failures metric: %v", err)
		return
	}
	c.Inc()
}

func (m *Metrics) MetricImageLayerReuseInc(layer string) {
	c, err := m.metricImageLayerReuse.GetMetricWithLabelValues(layer)
	if err != nil {
		logrus.Warnf("Unable to write image layer reuse metric: %v", err)
		return
	}
	c.Inc()
}

func (m *Metrics) MetricImagePullsSuccessesInc(name string) {
	c, err := m.metricImagePullsSuccesses.GetMetricWithLabelValues(name)
	if err != nil {
		logrus.Warnf("Unable to write image pull successes metric: %v", err)
		return
	}
	c.Inc()
}

func (m *Metrics) MetricImagePullsByDigestAdd(add float64, values ...string) {
	c, err := m.metricImagePullsByDigest.GetMetricWithLabelValues(values...)
	if err != nil {
		logrus.Warnf("Unable to write image pulls by digest metric: %v", err)
		return
	}
	c.Add(add)
}

func (m *Metrics) MetricImagePullsByNameAdd(add float64, values ...string) {
	c, err := m.metricImagePullsByName.GetMetricWithLabelValues(values...)
	if err != nil {
		logrus.Warnf("Unable to write image pulls by name metric: %v", err)
		return
	}
	c.Add(add)
}

// createEndpoint creates a /metrics endpoint for prometheus monitoring.
func (m *Metrics) createEndpoint() (*http.ServeMux, error) {
	for _, collector := range []prometheus.Collector{
		m.metricOperations,
		m.metricOperationsLatency,
		m.metricOperationsLatencyTotal,
		m.metricOperationsErrors,
		m.metricImagePullsByDigest,
		m.metricImagePullsByName,
		m.metricImagePullsByNameSkipped,
		m.metricImagePullsFailures,
		m.metricImagePullsSuccesses,
		m.metricImagePullsLayerSize,
		m.metricImageLayerReuse,
		m.metricContainersOOMTotal,
		m.metricContainersOOM,
	} {
		if err := m.impl.Register(collector); err != nil {
			return nil, errors.Wrap(err, "register metric")
		}
	}

	mux := &http.ServeMux{}
	mux.Handle("/metrics", promhttp.Handler())
	return mux, nil
}

func (m *Metrics) startEndpoint(
	stop chan struct{}, network, address string, me http.Handler,
) error {
	l, err := m.impl.Listen(network, address)
	if err != nil {
		return errors.Wrap(err, "creating listener")
	}

	go func() {
		var err error
		if m.config.MetricsCert != "" && m.config.MetricsKey != "" {
			logrus.Infof("Serving metrics on %s via HTTPs", address)

			kpr, reloadErr := newCertReloader(
				m.impl, stop, m.config.MetricsCert, m.config.MetricsKey,
			)
			if reloadErr != nil {
				logrus.Fatalf("Creating key pair reloader: %v", reloadErr)
			}

			srv := &http.Server{
				Handler: me,
				TLSConfig: &tls.Config{
					GetCertificate: kpr.getCertificate,
					MinVersion:     tls.VersionTLS12,
				},
			}
			err = m.impl.ServeTLS(srv, l, m.config.MetricsCert, m.config.MetricsKey)
		} else {
			logrus.Infof("Serving metrics on %s via HTTP", address)
			err = m.impl.Serve(l, me)
		}

		if err != nil {
			logrus.Fatalf("Failed to serve metrics endpoint %v: %v", l, err)
		}
		m.finished <- true
	}()

	return nil
}

type certReloader struct {
	impl        Impl
	certLock    sync.RWMutex
	certificate *tls.Certificate
	certPath    string
	keyPath     string
}

func newCertReloader(
	impl Impl, doneChan chan struct{}, certPath, keyPath string,
) (*certReloader, error) {
	reloader := &certReloader{
		impl:     impl,
		certPath: certPath,
		keyPath:  keyPath,
	}

	// Generate self-signed certificate and key if the provided ones are not
	// available.
	_, errCertPath := os.Stat(certPath)
	_, errKeyPath := os.Stat(keyPath)
	if errCertPath != nil && os.IsNotExist(errCertPath) &&
		errKeyPath != nil && os.IsNotExist(errKeyPath) {
		logrus.Info("Metrics key and cert path does not exist, generating self-signed")

		hostname, err := os.Hostname()
		if err != nil {
			return nil, errors.Wrap(err, "retrieve hostname")
		}

		certBytes, keyBytes, err := cert.GenerateSelfSignedCertKey(hostname, nil, nil)
		if err != nil {
			return nil, errors.Wrap(err, "generate self-signed cert/key")
		}

		for path, bytes := range map[string][]byte{
			certPath: certBytes,
			keyPath:  keyBytes,
		} {
			if err := os.MkdirAll(filepath.Dir(path), os.FileMode(0o700)); err != nil {
				return nil, errors.Wrap(err, "create path")
			}
			if err := ioutil.WriteFile(path, bytes, os.FileMode(0o600)); err != nil {
				return nil, errors.Wrap(err, "write file")
			}
		}
	}

	if err := reloader.reload(); err != nil {
		return nil, errors.Wrap(err, "load certificate")
	}

	watcher, err := reloader.impl.NewWatcher()
	if err != nil {
		return nil, errors.Wrap(err, "create new watcher")
	}
	go func() {
		defer watcher.Close()
		done := make(chan struct{})
		go func() {
			for {
				select {
				case event := <-watcher.Events:
					logrus.Debugf(
						"Got cert watcher event for %s (%s), reloading certificates",
						event.Name, event.Op.String(),
					)
					if err := reloader.reload(); err != nil {
						logrus.Warnf("Keeping previous certificates: %v", err)
					}
				case err := <-watcher.Errors:
					logrus.Errorf("Cert watcher error: %v", err)
					close(done)
					return
				case <-doneChan:
					logrus.Debug("Closing cert watcher")
					close(done)
					return
				}
			}
		}()
		for _, f := range []string{certPath, keyPath} {
			logrus.Debugf("Watching file %s for changes", f)
			if err := reloader.impl.Add(watcher, f); err != nil {
				logrus.Fatalf("Unable to watch %s: %v", f, err)
			}
		}
		<-done
	}()

	return reloader, nil
}

func (c *certReloader) reload() error {
	certificate, err := c.impl.LoadX509KeyPair(c.certPath, c.keyPath)
	if err != nil {
		return errors.Wrap(err, "load x509 key pair")
	}
	if len(certificate.Certificate) == 0 {
		return errors.New("certificates chain is empty")
	}

	x509Cert, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return errors.Wrap(err, "parse x509 certificate")
	}
	logrus.Infof(
		"Metrics certificate is valid between %v and %v",
		x509Cert.NotBefore, x509Cert.NotAfter,
	)

	now := time.Now()
	if now.After(x509Cert.NotAfter) {
		return errors.New("certificate is not valid any more")
	}
	if now.Before(x509Cert.NotBefore) {
		return errors.New("certificate is not yet valid")
	}

	c.certLock.Lock()
	c.certificate = &certificate
	c.certLock.Unlock()

	return nil
}

func (c *certReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.certLock.RLock()
	defer c.certLock.RUnlock()
	return c.certificate, nil
}
