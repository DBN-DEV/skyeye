package agent

import (
	"fmt"
	"net"

	"github.com/DBN-DEV/skyeye/pb"
)

func networkInterfaces() ([]*pb.NetworkInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("agent: list network interfaces: %w", err)
	}

	result := make([]*pb.NetworkInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("agent: get addresses for interface %s: %w", iface.Name, err)
		}

		addrStrings := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			addrStrings = append(addrStrings, addr.String())
		}

		result = append(result, &pb.NetworkInterface{Name: iface.Name, Addrs: addrStrings})
	}

	return result, nil
}
