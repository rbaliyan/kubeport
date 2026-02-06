package daemon

import (
	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
	"github.com/rbaliyan/kubeport/internal/proxy"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func convertForwardStatus(fs proxy.ForwardStatus) *kubeportv1.ForwardStatusProto {
	var errStr string
	if fs.Error != nil {
		errStr = fs.Error.Error()
	}

	return &kubeportv1.ForwardStatusProto{
		Service: &kubeportv1.ServiceInfo{
			Name:       fs.Service.Name,
			Service:    fs.Service.Service,
			Pod:        fs.Service.Pod,
			LocalPort:  int32(fs.Service.LocalPort),
			RemotePort: int32(fs.Service.RemotePort),
			Namespace:  fs.Service.Namespace,
		},
		State:      convertState(fs.State),
		Error:      errStr,
		Restarts:   int32(fs.Restarts),
		LastStart:  timestamppb.New(fs.LastStart),
		Connected:  fs.Connected,
		ActualPort: int32(fs.ActualPort),
	}
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
