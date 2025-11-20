package kpath

import "path"

func AgentAttr(agentID string) string {
	if len(agentID) == 0 {
		panic("agentID is empty")
	}

	return path.Join("/agent", agentID)
}

func AgentHeartbeat(agentID string) string {
	if len(agentID) == 0 {
		panic("agentID is empty")
	}

	return path.Join("/agent", agentID, "/heartbeat")
}
