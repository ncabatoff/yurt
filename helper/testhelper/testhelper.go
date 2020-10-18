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

func PromQueryActiveJob(ctx context.Context, addr string, job string) error {
	cli, err := promapi.NewClient(promapi.Config{Address: addr})
	if err != nil {
		return err
	}
	api := v1.NewAPI(cli)
	targs, err := api.Targets(ctx)
	if err != nil {
		return err
	}
	for _, target := range targs.Active {
		for k, v := range target.Labels {
			if k == "job" && string(v) == job {
				return nil
			}
		}
	}
	return fmt.Errorf("did not find active target for job %q", job)
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
