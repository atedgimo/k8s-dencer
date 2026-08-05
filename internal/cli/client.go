// Package cli implements the dencer command-line client.
//
// It is a client of the REST API, not a second planner. Everything it shows
// was computed by the planner and everything it asks for is authorised by the
// same SubjectAccessReview the UI goes through. Re-deriving a plan locally
// would put two implementations of the safety rails in the product, and the
// interesting failure mode would be the two disagreeing about whether a step
// is Red.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	// Legacy OIDC kubeconfigs name an auth-provider rather than an exec plugin.
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"
)

const defaultOperatorSA = "dencer-operator"

// Client talks to a k8s-dencer ui-backend.
type Client struct {
	base   string
	token  string
	http   *http.Client
	closer func()
}

// Config selects how to reach the backend and how to authenticate.
type Config struct {
	// Server is an explicit base URL. When empty the client port-forwards to
	// the Service through the kubeconfig, so the common case needs no setup.
	Server string

	// Token is a bearer token. When empty the client tries to find one in the
	// kubeconfig.
	Token string

	Namespace  string
	Release    string
	Kubeconfig string
	Context    string
	Timeout    time.Duration
	Insecure   bool
}

// Connect establishes a client.
//
// The default path deliberately does the awkward thing for the user: it finds
// the Service and port-forwards to it, the same way `kubectl port-forward`
// would, so `dencer plan` works on a fresh install with no flags. An operator
// who has exposed the backend through an Ingress passes --server and none of
// this runs.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	c := &Client{
		http:   &http.Client{Timeout: cfg.Timeout},
		closer: func() {},
	}

	if cfg.Server != "" {
		c.base = strings.TrimRight(cfg.Server, "/")
		c.token = cfg.Token
		if c.token == "" {
			c.token = os.Getenv("DENCER_TOKEN")
		}
		return c, nil
	}

	restCfg, err := restConfig(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Insecure {
		restCfg.Insecure = true
		restCfg.CAData = nil
		restCfg.CAFile = ""
	}

	c.token = cfg.Token
	if c.token == "" {
		c.token = os.Getenv("DENCER_TOKEN")
	}
	if c.token == "" {
		c.token, err = resolveToken(restCfg, cfg.Namespace)
		if err != nil {
			return nil, err
		}
	}

	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}

	local, stop, err := forward(ctx, restCfg, clientset, cfg.Namespace, cfg.Release)
	if err != nil {
		return nil, err
	}
	c.base = fmt.Sprintf("http://localhost:%d", local)
	c.closer = stop
	return c, nil
}

// Close releases the port-forward, if one was opened.
func (c *Client) Close() {
	if c.closer != nil {
		c.closer()
	}
}

func restConfig(cfg Config) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if cfg.Kubeconfig != "" {
		rules.ExplicitPath = cfg.Kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if cfg.Context != "" {
		overrides.CurrentContext = cfg.Context
	}
	rc, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("no usable kubeconfig: %w", err)
	}
	return rc, nil
}

// resolveToken finds a bearer token the ui-backend can verify with TokenReview.
//
// A static token in the kubeconfig is used when present. Credential plugins
// (gcloud, aws, az, OIDC) only materialise a token when client-go builds a
// transport — the same path kubectl uses — so tokenViaTransport runs the
// plugin once and captures the Authorization header. Client-certificate
// kubeconfigs — the default on k3d, kind and OrbStack — have no bearer token;
// rather than fail with "unauthenticated" from the server, say so here and
// name the fix.
func resolveToken(cfg *rest.Config, ns string) (string, error) {
	if cfg.BearerToken != "" {
		return cfg.BearerToken, nil
	}
	if cfg.BearerTokenFile != "" {
		b, err := os.ReadFile(cfg.BearerTokenFile)
		if err == nil && len(bytes.TrimSpace(b)) > 0 {
			return string(bytes.TrimSpace(b)), nil
		}
	}
	if cfg.ExecProvider != nil {
		fmt.Fprintf(os.Stderr, "running kubeconfig credential plugin: %s\n", cfg.ExecProvider.Command)
		tok, err := tokenViaTransport(cfg)
		if err != nil {
			return "", fmt.Errorf(
				"could not run your kubeconfig credential plugin: %w\n"+
					"Pass a token explicitly:\n"+
					"  dencer --token \"$(kubectl create token %s -n %s)\" ...\n"+
					"or set DENCER_TOKEN.", err, defaultOperatorSA, ns)
		}
		return tok, nil
	}
	if cfg.AuthProvider != nil {
		fmt.Fprintf(os.Stderr, "running kubeconfig credential plugin: %s\n", cfg.AuthProvider.Name)
		tok, err := tokenViaTransport(cfg)
		if err != nil {
			return "", fmt.Errorf(
				"could not run your kubeconfig credential plugin: %w\n"+
					"Pass a token explicitly:\n"+
					"  dencer --token \"$(kubectl create token %s -n %s)\" ...\n"+
					"or set DENCER_TOKEN.", err, defaultOperatorSA, ns)
		}
		return tok, nil
	}
	return "", errors.New(
		"your kubeconfig authenticates with a client certificate, and the backend verifies\n" +
			"identity with TokenReview, which only accepts bearer tokens. A certificate proves who\n" +
			"you are to the API server but cannot be reviewed as a token.\n\n" +
			"Mint one:\n" +
			"  dencer --token \"$(kubectl create token dencer-operator -n k8s-dencer)\" plan\n" +
			"or set DENCER_TOKEN once for the session.")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// tokenViaTransport invokes the kubeconfig credential plugin once and returns
// the bearer token it attaches to requests.
func tokenViaTransport(cfg *rest.Config) (string, error) {
	var tok string
	wrapped := *cfg
	wrapped.WrapTransport = func(http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if h := req.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				tok = strings.TrimPrefix(h, "Bearer ")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("{}")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})
	}
	rt, err := rest.TransportFor(&wrapped)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(cfg.Host, "/")+"/version", nil)
	if err != nil {
		return "", err
	}
	if _, err := rt.RoundTrip(req); err != nil {
		return "", err
	}
	if tok == "" {
		return "", errors.New("credential plugin produced no bearer token")
	}
	return tok, nil
}

// forward opens a port-forward to the ui-backend Service.
func forward(ctx context.Context, cfg *rest.Config, cs kubernetes.Interface, ns, release string) (int, func(), error) {
	svcName := release + "-ui-backend"
	svc, err := cs.CoreV1().Services(ns).Get(ctx, svcName, metav1.GetOptions{})
	if err != nil {
		return 0, nil, fmt.Errorf(
			"could not find Service %s/%s: %w\n"+
				"Pass --namespace/--release if the release is named differently, or --server if the\n"+
				"backend is reachable directly", ns, svcName, err)
	}
	port := int32(8080)
	if len(svc.Spec.Ports) > 0 {
		port = svc.Spec.Ports[0].Port
	}

	// Forwarding addresses a pod, not a Service. Picking the pod here rather
	// than shelling out to kubectl keeps this a single static binary.
	sel := labelSelector(svc.Spec.Selector)
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return 0, nil, fmt.Errorf("list backend pods: %w", err)
	}
	var target *corev1.Pod
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			target = &pods.Items[i]
			break
		}
	}
	if target == nil {
		return 0, nil, fmt.Errorf("no running %s pod in namespace %s", svcName, ns)
	}

	targetPort := port
	if len(svc.Spec.Ports) > 0 && svc.Spec.Ports[0].TargetPort.IntValue() != 0 {
		targetPort = int32(svc.Spec.Ports[0].TargetPort.IntValue())
	}

	roundTripper, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return 0, nil, fmt.Errorf("spdy transport: %w", err)
	}
	host := strings.TrimLeft(cfg.Host, "htps:/")
	serverURL := url.URL{
		Scheme: "https",
		Path:   fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", ns, target.Name),
		Host:   host,
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, &serverURL)

	// Port 0 lets the kernel choose, so two dencer invocations cannot collide
	// and neither collides with a `make ui` the operator left running.
	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", targetPort)}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return 0, nil, fmt.Errorf("port-forward: %w", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- fw.ForwardPorts() }()

	select {
	case <-readyCh:
	case err := <-errCh:
		return 0, nil, fmt.Errorf("port-forward failed: %w", err)
	case <-time.After(20 * time.Second):
		close(stopCh)
		return 0, nil, errors.New("timed out opening a port-forward to the backend")
	}

	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		return 0, nil, errors.New("port-forward opened but reported no local port")
	}
	return int(ports[0].Local), func() { close(stopCh) }, nil
}

func labelSelector(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

// get fetches a JSON document into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return fmt.Errorf("timed out talking to %s", c.base)
		}
		return fmt.Errorf("cannot reach %s: %w", c.base, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 400 {
		return apiError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unexpected response from %s: %w", path, err)
	}
	return nil
}

// apiError turns the server's error envelope into something worth reading.
func apiError(status int, raw []byte) error {
	var e struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	msg := e.Error
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("not authenticated: %s\nThe token was rejected. Mint a fresh one:\n"+
			"  export DENCER_TOKEN=\"$(kubectl create token dencer-operator -n k8s-dencer)\"", msg)
	case http.StatusForbidden:
		// Naming the verb matters: the fix is a RoleBinding, and the user
		// cannot guess which one from "forbidden".
		return fmt.Errorf("not authorized: %s\nExecution needs 'create consolidations.dencer.io';\n"+
			"reading needs 'get plans.dencer.io'. Both are checked against cluster RBAC", msg)
	case http.StatusNotFound:
		return fmt.Errorf("not found: %s", msg)
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("%s (HTTP %d)", msg, status)
}
