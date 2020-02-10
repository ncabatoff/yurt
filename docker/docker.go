package docker

import (
	"context"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

func SetupNetwork(ctx context.Context, cli *client.Client, netName, cidr string) (string, error) {
	netResources, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return "", err
	}
	for _, netRes := range netResources {
		if netRes.Name == netName {
			if len(netRes.IPAM.Config) > 0 && netRes.IPAM.Config[0].Subnet == cidr {
				return netRes.ID, nil
			}
			_ = cli.NetworkRemove(ctx, netRes.ID)
		}
	}

	id, err := createNetwork(ctx, cli, netName, cidr)
	if err != nil {
		return "", err
	}
	return id, nil
}

func createNetwork(ctx context.Context, cli *client.Client, netName, cidr string) (string, error) {
	resp, err := cli.NetworkCreate(ctx, netName, types.NetworkCreate{
		CheckDuplicate: true,
		Driver:         "bridge",
		Options:        map[string]string{},
		IPAM: &network.IPAM{
			Driver:  "default",
			Options: map[string]string{},
			Config: []network.IPAMConfig{
				{
					Subnet: cidr,
				},
			},
		},
	})
	if err != nil {
		return "", err
	}

	return resp.ID, nil
}
