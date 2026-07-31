package window

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/atedgimo/k8s-dencer/api/v1alpha1"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Reader loads maintenance windows from the cluster and evaluates them.
//
// Uncached, like everything else the executor reads: a window that was
// suspended thirty seconds ago must be suspended now. The whole point of the
// object is to be a live authorisation, and a cached copy of one is not.
type Reader struct {
	client client.Client
	log    *slog.Logger
	now    func() time.Time
}

// NewReader builds a reader over a REST config.
func NewReader(cfg *rest.Config, log *slog.Logger) (*Reader, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	return &Reader{client: c, log: log, now: time.Now}, nil
}

// Current evaluates every window in the cluster at this instant.
//
// A cluster with the CRD absent is not an error: it is an install that has
// never created a window, which is the majority. That resolves to an empty set
// and therefore to "Red is refused", which is the correct answer.
func (r *Reader) Current(ctx context.Context) (*Set, error) {
	var list v1alpha1.MaintenanceWindowList
	if err := r.client.List(ctx, &list); err != nil {
		if crdAbsent(err) {
			return EvaluateAll(nil, r.now()), nil
		}
		return nil, fmt.Errorf("list maintenance windows: %w", err)
	}
	return EvaluateAll(list.Items, r.now()), nil
}

// AllowsRedOn satisfies safety.Windows by reading fresh state per call.
//
// Errors resolve to "refused", with the error in the reason. A window is an
// authorisation, and being unable to read one is not the same as having one.
func (r *Reader) AllowsRedOn(ctx context.Context, node model.Node) (bool, string) {
	// The caller's context, capped: a wedged API server must not stall a step
	// for longer than this, but an operator aborting the run must not have to
	// wait even that long — cancellation propagates from the run itself.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	set, err := r.Current(ctx)
	if err != nil {
		r.log.Error("could not read maintenance windows", "error", err)
		return false, fmt.Sprintf("maintenance windows could not be read (%v), so Red stays refused", err)
	}
	return set.AllowsRedOn(node)
}

// UpdateStatus writes each window's evaluated state back to its status, so
// `kubectl get mw` answers "is it open right now" without anyone reasoning
// about cron in their head.
func (r *Reader) UpdateStatus(ctx context.Context) error {
	var list v1alpha1.MaintenanceWindowList
	if err := r.client.List(ctx, &list); err != nil {
		if crdAbsent(err) {
			return nil
		}
		return err
	}

	now := r.now()
	for i := range list.Items {
		mw := &list.Items[i]
		s := Evaluate(*mw, now)

		next := mw.Status.DeepCopy()
		next.Active = s.Open
		next.Message = s.Reason
		next.ClosesAt = timePtr(s.ClosesAt)
		next.NextOpen = timePtr(s.NextOpen)
		if equalStatus(&mw.Status, next) {
			continue
		}
		mw.Status = *next
		if err := r.client.Status().Update(ctx, mw); err != nil {
			r.log.Warn("could not update window status", "window", mw.Name, "error", err)
		}
	}
	return nil
}

// Run keeps window status current.
func (r *Reader) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.UpdateStatus(ctx); err != nil {
				r.log.Error("window status sweep failed", "error", err)
			}
		}
	}
}

func timePtr(t time.Time) *metav1.Time {
	if t.IsZero() {
		return nil
	}
	mt := metav1.NewTime(t)
	return &mt
}

func equalStatus(a, b *v1alpha1.MaintenanceWindowStatus) bool {
	return a.Active == b.Active && a.Message == b.Message &&
		sameTime(a.ClosesAt, b.ClosesAt) && sameTime(a.NextOpen, b.NextOpen)
}

func sameTime(a, b *metav1.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(b)
}

// crdAbsent reports whether the failure is simply that MaintenanceWindow is not
// installed — the state of every cluster that has never created one, and not an
// error. It resolves to an empty set, and therefore to "Red is refused".
func crdAbsent(err error) bool {
	if err == nil {
		return false
	}
	if apierrors.IsNotFound(err) {
		return true
	}
	// meta.NoKindMatchError carries no APIStatus, so it is matched by type.
	var noMatch *meta.NoKindMatchError
	return errors.As(err, &noMatch)
}
