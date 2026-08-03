package kube

import (
	"context"
	"sync"

	"github.com/lab-paper-code/chill/internal/workloadobserver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

func classifyWatchError(object runtime.Object) (workloadobserver.FailureReason, string) {
	err := apierrors.FromObject(object)
	if apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
		return workloadobserver.FailureReasonHistoryExpired, err.Error()
	}
	return workloadobserver.FailureReasonWatchFailed, err.Error()
}

type continuingWatcher struct {
	ctx           context.Context
	source        PodSource
	fieldSelector string

	mu    sync.Mutex
	first watch.Interface
}

var _ cache.Watcher = (*continuingWatcher)(nil)

func (w *continuingWatcher) Watch(options metav1.ListOptions) (watch.Interface, error) {
	w.mu.Lock()
	if w.first != nil {
		first := w.first
		w.first = nil
		w.mu.Unlock()
		return first, nil
	}
	w.mu.Unlock()

	options.FieldSelector = w.fieldSelector
	options.AllowWatchBookmarks = true
	return w.source.Watch(w.ctx, options)
}
