package kube

import (
	"context"
	"runtime"
	"testing"
)

func TestSchedulerCapacityWorkersFollowAcceptedConfigurationAndSettings(t *testing.T) {
	c := schedulerEmptyPoolController(t)
	automatic := max(1, runtime.GOMAXPROCS(0)/2)
	if status := c.CapacityWorkers(); status.Limit != automatic || status.Active != 0 {
		t.Fatalf("constructor ignored automatic worker policy: %+v", status)
	}
	config := c.config
	config.Settings.CapacityWorkers = 3
	if err := c.SetConfiguration(config); err != nil {
		t.Fatal(err)
	}
	if status := c.CapacityWorkers(); status.Limit != 3 {
		t.Fatalf("configuration acceptance did not update live admission: %+v", status)
	}
	settings := c.Settings()
	for _, workers := range []int{1, 0} {
		settings.CapacityWorkers = workers
		if err := c.AcceptSettings(context.Background(), settings, config.Preparations); err != nil {
			t.Fatal(err)
		}
		want := workers
		if want == 0 {
			want = automatic
		}
		if status := c.CapacityWorkers(); status.Limit != want {
			t.Fatalf("settings acceptance did not update live admission: %+v want=%d", status, want)
		}
	}
	settings.CapacityWorkers = -1
	if err := c.AcceptSettings(context.Background(), settings, config.Preparations); err == nil {
		t.Fatal("negative workers crossed settings acceptance")
	}
	config.Settings.CapacityWorkers = -1
	if err := c.SetConfiguration(config); err == nil {
		t.Fatal("negative workers crossed configuration acceptance")
	}
	if c.CapacityWorkers().Limit != automatic || c.Settings().CapacityWorkers != 0 {
		t.Fatal("rejected update changed accepted policy or live admission")
	}
}
