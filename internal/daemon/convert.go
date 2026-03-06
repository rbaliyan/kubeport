package daemon

import (
	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/config"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func convertForwardStatus(fs proxy.ForwardStatus) *kubeportv1.ForwardStatusProto {
	var errStr string
	if fs.Error != nil {
		errStr = fs.Error.Error()
	}

	proto := &kubeportv1.ForwardStatusProto{
		Service: &kubeportv1.ServiceInfo{
			Name:       fs.Service.Name,
			Service:    fs.Service.Service,
			Pod:        fs.Service.Pod,
			LocalPort:  int32(fs.Service.LocalPort),
			RemotePort: int32(fs.Service.RemotePort),
			Namespace:  fs.Service.Namespace,
			ParentName: fs.Service.ParentName,
			PortName:   fs.Service.PortName,
		},
		State:      convertState(fs.State),
		Error:      errStr,
		Restarts:   int32(fs.Restarts),
		LastStart:  timestamppb.New(fs.LastStart),
		Connected:  fs.Connected,
		ActualPort: int32(fs.ActualPort),
		BytesIn:    fs.BytesIn,
		BytesOut:   fs.BytesOut,
	}
	if !fs.NextRetry.IsZero() {
		proto.NextRetry = timestamppb.New(fs.NextRetry)
	}
	return proto
}

func serviceInfoToConfig(si *kubeportv1.ServiceInfo) config.ServiceConfig {
	svc := config.ServiceConfig{
		Name:       si.Name,
		Service:    si.Service,
		Pod:        si.Pod,
		LocalPort:  int(si.LocalPort),
		RemotePort: int(si.RemotePort),
		Namespace:  si.Namespace,
	}
	return svc
}

func portSpecToConfig(ps *kubeportv1.PortSpec) (config.PortsConfig, []string, int) {
	if ps == nil {
		return config.PortsConfig{}, nil, 0
	}
	pc := config.PortsConfig{All: ps.All}
	for _, name := range ps.PortNames {
		pc.Selectors = append(pc.Selectors, config.PortSelector{Name: name})
	}
	return pc, ps.ExcludePorts, int(ps.LocalPortOffset)
}

func convertState(s proxy.ForwardState) kubeportv1.ForwardState {
	switch s {
	case proxy.StateStarting:
		return kubeportv1.ForwardState_FORWARD_STATE_STARTING
	case proxy.StateRunning:
		return kubeportv1.ForwardState_FORWARD_STATE_RUNNING
	case proxy.StateFailed:
		return kubeportv1.ForwardState_FORWARD_STATE_FAILED
	case proxy.StateStopped:
		return kubeportv1.ForwardState_FORWARD_STATE_STOPPED
	default:
		return kubeportv1.ForwardState_FORWARD_STATE_UNSPECIFIED
	}
}
