package main

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	consolekube "github.com/lab-paper-code/chill/internal/operatorconsole/kube"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type compatibilityMemoKey struct {
	typeOf reflect.Type
	name   client.ObjectKey
}

type compatibilityMemoValue struct {
	object client.Object
	err    error
}

// One sequential matrix evaluation owns this reader. Inventory LIST objects
// seed exact GETs, including Nodes shared by multiple DeviceClasses.
type compatibilityMemoReader struct {
	client.Reader
	objects map[compatibilityMemoKey]compatibilityMemoValue
}

func (r *compatibilityMemoReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	identity := compatibilityMemoKey{reflect.TypeOf(object), key}
	value, found := r.objects[identity]
	if !found {
		err := r.Reader.Get(ctx, key, object, opts...)
		value = compatibilityMemoValue{object: object, err: err}
		r.objects[identity] = value
		return err
	}
	if value.err == nil {
		reflect.ValueOf(object).Elem().Set(reflect.ValueOf(value.object).Elem())
	}
	return value.err
}

func (r *compatibilityMemoReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return err
	}
	objects, err := meta.ExtractList(list)
	if err != nil {
		return err
	}
	for _, item := range objects {
		object := item.(client.Object)
		r.objects[compatibilityMemoKey{reflect.TypeOf(object), client.ObjectKeyFromObject(object)}] = compatibilityMemoValue{object: object}
	}
	return nil
}

func (r *compatibilityMemoReader) revision() string {
	identities := make([]string, 0, len(r.objects))
	for key, value := range r.objects {
		identity := key.typeOf.String() + ":" + key.name.String()
		if value.err == nil {
			identity += ":" + string(value.object.GetUID()) + ":" + value.object.GetResourceVersion()
		} else {
			identity += ":" + value.err.Error()
		}
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	return strings.Join(identities, "\x00")
}

func newModelCompatibilityMatrixQuery(reader client.Reader, opts options) func(context.Context) (operatorconsole.ModelCompatibilityMatrixView, error) {
	var mu sync.Mutex
	var revision string
	var cached operatorconsole.ModelCompatibilityMatrixView
	return func(ctx context.Context) (operatorconsole.ModelCompatibilityMatrixView, error) {
		mu.Lock()
		defer mu.Unlock()
		if err := ctx.Err(); err != nil {
			return operatorconsole.ModelCompatibilityMatrixView{}, err
		}
		memo := &compatibilityMemoReader{Reader: reader, objects: make(map[compatibilityMemoKey]compatibilityMemoValue)}
		inventoryQuery, err := consolekube.NewKubernetesInventoryQuery(memo)
		if err != nil {
			return operatorconsole.ModelCompatibilityMatrixView{}, err
		}
		inventory, err := inventoryQuery.Read(ctx)
		if err != nil {
			return operatorconsole.ModelCompatibilityMatrixView{}, err
		}
		facts := &corev1.ConfigMap{}
		err = memo.Get(ctx, client.ObjectKey{Namespace: opts.namespace, Name: opts.factSourceConfigMap}, facts)
		if err != nil && !apierrors.IsNotFound(err) {
			return operatorconsole.ModelCompatibilityMatrixView{}, fmt.Errorf("read compatibility facts: %w", err)
		}
		nextRevision := memo.revision()
		if revision == nextRevision {
			// A successful source check is not a change in static support.
			cached.ReadAt = inventory.ReadAt.Format(time.RFC3339Nano)
			return cached, nil
		}
		query, err := newModelCompatibilityQuery(memo, opts)
		if err != nil {
			return operatorconsole.ModelCompatibilityMatrixView{}, err
		}
		matrix, err := consolekube.BuildModelCompatibilityMatrix(ctx, inventory, query.Read)
		if err != nil {
			return operatorconsole.ModelCompatibilityMatrixView{}, err
		}
		revision, cached = nextRevision, matrix
		return matrix, nil
	}
}
