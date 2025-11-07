package raft

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocker(t *testing.T) {
	for name, tt := range map[string]struct {
		nodes int
	}{
		"13 node quorum": {
			nodes: 13,
		},
	} {
		t.Run(name, func(t *testing.T) {
			minQuorum := (tt.nodes + 1) / 2

			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()

			leaders := setupLockTest(t, ctx, tt.nodes)
			var currentLeader *testLeader
			currentLeaders := leaders
			// scale down til we get to the min followers
			for len(currentLeaders) != (minQuorum - 1) {
				currentLeader, currentLeaders = waitForAnyLeader(t, 15*time.Second, currentLeaders...)
				currentLeader.Stop()
				t.Log("killing leader", currentLeader.id)
			}
		})
	}
}

type testLeader struct {
	id uint64

	leader   chan struct{}
	follower chan struct{}
	onStop   chan struct{}
	err      chan error

	cancel      context.CancelFunc
	stopped     atomic.Bool
	isLeader    atomic.Bool
	initialized atomic.Bool
}

func newTestLeader(id uint64, cancel context.CancelFunc) *testLeader {
	return &testLeader{
		id:       id,
		leader:   make(chan struct{}, 1),
		follower: make(chan struct{}, 1),
		err:      make(chan error, 1),
		onStop:   make(chan struct{}, 1),
		cancel:   cancel,
	}
}

func (t *testLeader) OnStartedLeading(ctx context.Context) {
	t.initialized.Store(true)
	t.isLeader.Store(true)
	select {
	case t.leader <- struct{}{}:
	default:
	}
}

func (t *testLeader) OnStoppedLeading() {
	t.initialized.Store(true)
	t.isLeader.Store(false)

	select {
	case t.follower <- struct{}{}:
	default:
	}
}

func (t *testLeader) IsLeader() bool {
	return t.initialized.Load() && t.isLeader.Load()
}

func (t *testLeader) IsFollower() bool {
	return t.initialized.Load() && !t.isLeader.Load()
}

func (t *testLeader) HandleError(err error) {
	if err == nil {
		return
	}

	select {
	case t.err <- err:
	default:
	}
}

func (t *testLeader) IsStopped() bool {
	return t.stopped.Load()
}

func (t *testLeader) Stop() {
	t.cancel()

	select {
	case t.onStop <- struct{}{}:
	default:
	}
	t.stopped.Store(true)
}

func (t *testLeader) WaitForLeader(tst *testing.T, timeout time.Duration) {
	tst.Helper()

	select {
	case <-t.leader:
	case err := <-t.err:
		tst.Fatalf("leader election failed: %v", err)
	case <-time.After(timeout):
		tst.Fatalf("timed out waiting for leader election to start")
	}
}

func (t *testLeader) WaitForFollower(tst *testing.T, timeout time.Duration) {
	tst.Helper()

	select {
	case <-t.follower:
	case err := <-t.err:
		tst.Fatalf("leader election failed: %v", err)
	case <-time.After(timeout):
		tst.Fatalf("timed out waiting for leader election to stop")
	}
}

func (t *testLeader) WaitForStopped(tst *testing.T, timeout time.Duration) {
	tst.Helper()

	select {
	case <-t.onStop:
	case err := <-t.err:
		tst.Fatalf("leader election failed: %v", err)
	case <-time.After(timeout):
		tst.Fatalf("timed out waiting for leader election to stop")
	}
}

func (t *testLeader) Error() error {
	select {
	case err := <-t.err:
		return err
	default:
		return nil
	}
}

func waitForAnyLeader(t *testing.T, timeout time.Duration, leaders ...*testLeader) (*testLeader, []*testLeader) {
	cases := make([]reflect.SelectCase, len(leaders))
	for i, leader := range leaders {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(leader.leader)}
	}
	timeoutCh := time.After(timeout)
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(timeoutCh)})
	chosen, _, _ := reflect.Select(cases)
	if chosen == len(cases)-1 {
		t.Fatalf("timed out waiting for leader to be elected")
	}

	chosenLeader := leaders[chosen]

	followers := []*testLeader{}
	pending := []*testLeader{}
	for i, leader := range leaders {
		if chosen == i {
			continue
		}
		followers = append(followers, leader)
		if leader.IsFollower() {
			continue
		}
		pending = append(pending, leader)
	}

	if len(pending) == 0 {
		return chosenLeader, followers
	}

	followed := 0
	cases = make([]reflect.SelectCase, len(pending))
	for i, leader := range pending {
		cases[i] = reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(leader.follower)}
	}
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(timeoutCh)})

	for {
		chosen, _, _ := reflect.Select(cases)
		if chosen == len(cases)-1 {
			t.Fatalf("timed out waiting for followers to become active")
		}
		followed++
		if followed >= len(pending) {
			return chosenLeader, followers
		}
	}
}

func setupLockTest(t *testing.T, ctx context.Context, n int) []*testLeader {
	if n <= 0 {
		t.Fatalf("at least one lock configuration is required")
	}

	ports := getFreePorts(t, n)

	leaders := []*testLeader{}
	nodes := []*LockerNode{}
	for i, port := range ports {
		nodes = append(nodes, &LockerNode{
			ID:      uint64(i + 1),
			Address: fmt.Sprintf("127.0.0.1:%d", port),
		})
	}

	ca, certificates := generateCertificates(t, nodes)

	configs := []LockerConfig{}
	for i, node := range nodes {
		peers := []*LockerNode{}
		for _, peer := range nodes {
			peers = append(peers, peer)
		}

		configs = append(configs, LockerConfig{
			ID:                uint64(i + 1),
			Address:           node.Address,
			Peers:             peers,
			CA:                ca,
			PrivateKey:        certificates[i].privateKey,
			Certificate:       certificates[i].certificate,
			ElectionTimeout:   1 * time.Second,
			HeartbeatInterval: 100 * time.Millisecond,
		})
	}

	for _, config := range configs {
		ctx, cancel := context.WithCancel(ctx)
		leader := newTestLeader(config.ID, cancel)
		callbacks := &LeaderCallbacks{
			OnStartedLeading: leader.OnStartedLeading,
			OnStoppedLeading: leader.OnStoppedLeading,
		}

		leaders = append(leaders, leader)

		go func() {
			defer leader.Stop()
			defer t.Log("leader election goroutine exiting")

			leader.HandleError(Run(ctx, config, callbacks))
		}()
	}

	return leaders
}

func getFreePorts(t *testing.T, n int) []int {
	ports := make([]int, 0, n)
	listeners := make([]net.Listener, 0, n)

	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("error getting free port: %v", err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}

	for _, l := range listeners {
		l.Close()
	}

	return ports
}

type certificate struct {
	privateKey  []byte
	certificate []byte
}

func generateCertificates(t *testing.T, nodes []*LockerNode) ([]byte, []certificate) {
	caPEM, _, ca, caPK := generateCA(t)

	certificates := []certificate{}
	for range nodes {
		pem, pkPEM := generateSignedCert(t, "127.0.0.1", ca, caPK)
		certificates = append(certificates, certificate{
			privateKey:  pkPEM,
			certificate: pem,
		})
	}

	return caPEM, certificates
}

func generateCA(t *testing.T) ([]byte, []byte, *x509.Certificate, *ecdsa.PrivateKey) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("error generating CA: %v", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	caTemplate := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "test",
			Organization: []string{"test"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(10 * 365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("error creating CA certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	keyBytes, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		t.Fatalf("error marshaling CA: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	caCert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("error parsing CA: %v", err)
	}

	return certPEM, keyPEM, caCert, caKey
}

func generateSignedCert(t *testing.T, host string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) ([]byte, []byte) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}

	serialNumber, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"test"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP(host)},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &tmpl, caCert, &priv.PublicKey, caKey)
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("generate marshaling cert: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM
}
