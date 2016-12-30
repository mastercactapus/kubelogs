package main

import (
	"flag"
	"net/url"
	"os"
	"path"
	"time"

	"context"

	log "github.com/sirupsen/logrus"
)

type options struct {
	u           url.URL
	since       time.Duration
	decode      bool
	mergeLabels bool
	clusterName string
	out         *log.Logger
}

func (o options) buildEventURL(apiPath string) string {
	v := make(url.Values, 1)
	v.Set("watch", "true")
	return o.buildEventURLWithQuery(apiPath, v)
}
func (o options) buildEventURLWithQuery(apiPath string, q url.Values) string {
	o.u.Path = path.Join(o.u.Path, apiPath)
	uq := o.u.Query()
	for key := range q {
		uq.Set(key, q.Get(key))
	}
	o.u.RawQuery = uq.Encode()
	return o.u.String()
}

func (o options) newEventStream(ctx context.Context, apiPath string) (*eventStream, error) {
	return newEventStream(ctx, o.buildEventURL(apiPath))
}

func main() {
	k8sURL := flag.String("url", "http://127.0.0.1:8001/", "Kubernetes API URL")
	jsonOutput := flag.Bool("json", false, "JSON output format")
	verbose := flag.Bool("v", false, "Enable verbose logging")
	since := flag.Duration("since", -1, "Only accept logs from this time on. If negative, this filter is ignored.")
	decode := flag.Bool("decode", true, "Decode JSON-formatted messages for annotated pods, values will be under the 'event' key.")
	mergeLabels := flag.Bool("labels", true, "Merge pod labels, values will be under the 'labels' key.")
	clusterName := flag.String("cluster", "default", "Cluster name. Used as a label for events")
	flag.Parse()

	if *jsonOutput {
		log.SetFormatter(&log.JSONFormatter{})
	}
	if *verbose {
		log.SetLevel(log.DebugLevel)
	}

	u, err := url.Parse(*k8sURL)
	if err != nil {
		log.WithField("URL", *k8sURL).Fatalln("invalid URL:", err)
	}
	opt := &options{
		u:           *u,
		since:       *since,
		decode:      *decode,
		mergeLabels: *mergeLabels,
		out:         log.New(),
		clusterName: *clusterName,
	}
	opt.out.Out = os.Stdout
	if *jsonOutput {
		opt.out.Formatter = &log.JSONFormatter{}
	}

	c, err := newCluster(opt)
	if err != nil {
		log.WithField("URL", *k8sURL).Fatalln(err)
	}
	c.loop()
}
