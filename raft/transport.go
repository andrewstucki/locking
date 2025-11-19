package raft

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"sync"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	transportv1 "github.com/andrewstucki/locking/raft/proto/gen/transport/v1"
	"google.golang.org/grpc/status"
)

type peer struct {
	addr   string
	client transportv1.TransportServiceClient
}

func newPeer(addr string, credentials credentials.TransportCredentials) (*peer, error) {
	if credentials == nil {
		credentials = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials))
	if err != nil {
		return nil, err
	}

	return &peer{
		addr:   addr,
		client: transportv1.NewTransportServiceClient(conn),
	}, nil
}

func ClientFor(config LockConfiguration, node LockerNode) (transportv1.TransportServiceClient, error) {
	var err error
	var credentials credentials.TransportCredentials

	if config.Insecure {
		credentials = insecure.NewCredentials()
	} else {
		credentials, err = clientTLSConfig(config.Certificate, config.PrivateKey, config.CA)
	}

	if err != nil {
		return nil, fmt.Errorf("unable to initialize client credentials: %w", err)
	}

	conn, err := grpc.NewClient(node.Address, grpc.WithTransportCredentials(credentials))
	if err != nil {
		return nil, err
	}
	return transportv1.NewTransportServiceClient(conn), nil
}

type KubeconfigFetcher interface {
	Fetch(context.Context) ([]byte, error)
}

type KubeconfigFetcherFn func(context.Context) ([]byte, error)

func (fn KubeconfigFetcherFn) Fetch(ctx context.Context) ([]byte, error) {
	return fn(ctx)
}

type grpcTransport struct {
	addr  string
	peers map[uint64]*peer

	node     raft.Node
	nodeLock sync.RWMutex

	kubeconfigFetcher KubeconfigFetcher

	serverCredentials credentials.TransportCredentials
	clientCredentials credentials.TransportCredentials

	logger raft.Logger

	transportv1.UnimplementedTransportServiceServer
}

func newGRPCTransport(certPEM, keyPEM, caPEM []byte, addr string, peers map[uint64]string, fetcher KubeconfigFetcher) (*grpcTransport, error) {
	serverCredentials, err := serverTLSConfig(certPEM, keyPEM, caPEM)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize server credentials: %w", err)
	}
	clientCredentials, err := clientTLSConfig(certPEM, keyPEM, caPEM)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize client credentials: %w", err)
	}

	initializedPeers := make(map[uint64]*peer, len(peers))
	for id, peer := range peers {
		initialized, err := newPeer(peer, clientCredentials)
		if err != nil {
			return nil, err
		}
		initializedPeers[id] = initialized
	}

	return &grpcTransport{
		addr:              addr,
		peers:             initializedPeers,
		serverCredentials: serverCredentials,
		clientCredentials: clientCredentials,
		kubeconfigFetcher: fetcher,
	}, nil
}

func newInsecureGRPCTransport(addr string, peers map[uint64]string, fetcher KubeconfigFetcher) (*grpcTransport, error) {
	initializedPeers := make(map[uint64]*peer, len(peers))
	for id, peer := range peers {
		initialized, err := newPeer(peer, nil)
		if err != nil {
			return nil, err
		}
		initializedPeers[id] = initialized
	}

	return &grpcTransport{
		addr:              addr,
		peers:             initializedPeers,
		kubeconfigFetcher: fetcher,
	}, nil
}

func (t *grpcTransport) setNode(node raft.Node) {
	t.nodeLock.Lock()
	defer t.nodeLock.Unlock()
	t.node = node
}

func (t *grpcTransport) getNode() raft.Node {
	t.nodeLock.RLock()
	defer t.nodeLock.RUnlock()
	return t.node
}

func (t *grpcTransport) DoSend(ctx context.Context, msg raftpb.Message) (bool, error) {
	peer, ok := t.peers[msg.To]
	if !ok {
		return false, fmt.Errorf("unknown peer %d", msg.To)
	}

	data, err := msg.Marshal()
	if err != nil {
		return false, fmt.Errorf("marshaling message for peer %q: %w", peer.addr, err)
	}

	resp, err := peer.client.Send(ctx, &transportv1.SendRequest{
		Payload: data,
	})
	if err != nil {
		return false, fmt.Errorf("sending to peer %q: %w", peer.addr, err)
	}

	return resp.Applied, nil
}

func (t *grpcTransport) Send(ctx context.Context, req *transportv1.SendRequest) (*transportv1.SendResponse, error) {
	if node := t.getNode(); node != nil {
		var msg raftpb.Message
		if err := msg.Unmarshal(req.Payload); err != nil {
			return &transportv1.SendResponse{Applied: false}, nil
		}
		if err := node.Step(ctx, msg); err == nil {
			return &transportv1.SendResponse{Applied: true}, nil
		}
	}
	return &transportv1.SendResponse{Applied: false}, nil
}

func (t *grpcTransport) Kubeconfig(ctx context.Context, req *transportv1.KubeconfigRequest) (*transportv1.KubeconfigResponse, error) {
	if t.kubeconfigFetcher == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "no kubeconfig fetcher specified")
	}
	data, err := t.kubeconfigFetcher.Fetch(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, err.Error())
	}

	return &transportv1.KubeconfigResponse{Payload: data}, nil
}

func (t *grpcTransport) Run(ctx context.Context) error {
	defer t.logger.Info("shutting down grpc transport")

	credentials := t.serverCredentials
	if credentials == nil {
		credentials = insecure.NewCredentials()
	}

	server := grpc.NewServer(grpc.Creds(credentials))
	transportv1.RegisterTransportServiceServer(server, t)

	lis, err := net.Listen("tcp", t.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", t.addr, err)
	}
	defer lis.Close()

	done := make(chan struct{})
	errs := make(chan error, 1)
	go func() {
		defer close(done)

		if err := server.Serve(lis); err != nil {
			errs <- err
		}
	}()

	select {
	case <-ctx.Done():
		server.GracefulStop()
		select {
		case err := <-errs:
			return err
		default:
			return nil
		}
	case err := <-errs:
		server.Stop()
		return err
	}
}

func serverTLSConfig(certPEM, keyPEM, caPEM []byte) (credentials.TransportCredentials, error) {
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to load server certificate: %w", err)
	}

	capool := x509.NewCertPool()
	if !capool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("unable to append the CA certificate to CA pool")
	}

	tlsConfig := &tls.Config{
		ClientAuth:   tls.RequireAndVerifyClientCert,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    capool,
	}
	return credentials.NewTLS(tlsConfig), nil
}

func clientTLSConfig(certPEM, keyPEM, caPEM []byte) (credentials.TransportCredentials, error) {
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to load client certificate: %w", err)
	}

	capool := x509.NewCertPool()
	if !capool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("unable to append the CA certificate to CA pool")
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{certificate},
		RootCAs:      capool,
	}
	return credentials.NewTLS(tlsConfig), nil
}
