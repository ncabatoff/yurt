package testhelper

import (
	"context"
	"fmt"
	"testing"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	"github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

func UntilPass(t *testing.T, ctx context.Context, f func() error) {
	t.Helper()
	lastErr := fmt.Errorf("timed out")
	for {
		time.Sleep(100 * time.Millisecond)
		err := f()
		switch {
		case err == nil:
			return
		case ctx.Err() != nil:
			t.Fatalf("timed out, last error: %v", lastErr)
		default:
			lastErr = err
		}
	}

}

func PromQueryActiveInstances(ctx context.Context, addr string, job string) ([]string, error) {
	cli, err := promapi.NewClient(promapi.Config{Address: addr})
	if err != nil {
		return nil, err
	}
	api := v1.NewAPI(cli)
	targs, err := api.Targets(ctx)
	if err != nil {
		return nil, err
	}
	var instances []string
	for _, target := range targs.Active {
		if string(target.Labels["job"]) == job {
			instances = append(instances, string(target.Labels["instance"]))
		}
	}
	return instances, err
}

func PromQueryVector(ctx context.Context, addr string, job string, metric string) ([]float64, error) {
	cli, err := promapi.NewClient(promapi.Config{Address: addr})
	if err != nil {
		return nil, err
	}
	api := v1.NewAPI(cli)
	query := fmt.Sprintf(`%s{job="%s"}`, metric, job)
	val, _, err := api.Query(ctx, query, time.Now())
	if err != nil {
		return nil, fmt.Errorf("query %q failed: %w", query, err)
	}
	vect, ok := val.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("query %q did not return a vector: %v", query, err)
	}

	var samples []float64
	for _, s := range vect {
		samples = append(samples, float64(s.Value))
	}
	return samples, nil
}

// PromQueryAlive makes sure that the job has count target instances and that the
// chosen canary metric is present for all of them.
func PromQueryAlive(ctx context.Context, addr string, job string, metric string, count int) error {
	instances, err := PromQueryActiveInstances(ctx, addr, job)
	if err != nil {
		return err
	}
	if len(instances) != count {
		return fmt.Errorf("expected %d instances, got %d", count, len(instances))
	}

	samples, err := PromQueryVector(ctx, addr, job, metric)
	if len(samples) != count {
		return fmt.Errorf("expected %d samples in vector for metric %q, got %d", count, metric, len(samples))
	}

	return nil
}
