package cluster

import (
	"fmt"
	"net"

	"google.golang.org/grpc"

	pb "github.com/FlameInTheDark/servy/internal/cluster/pb"
)

// ServeGRPC starts the NodeService server on addr (blocking).
func ServeGRPC(svc *Service, addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	server := grpc.NewServer()
	pb.RegisterNodeServiceServer(server, svc)
	fmt.Printf("cluster gRPC listening on %s\n", addr)
	return server.Serve(lis)
}
