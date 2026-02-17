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

func PingJobPrefix(agentID string) string {
	if len(agentID) == 0 {
		panic("agentID is empty")
	}

	return path.Join("/pingjob", agentID) + "/"
}

func PingJobKey(agentID, jobID string) string {
	if len(agentID) == 0 {
		panic("agentID is empty")
	}
	if len(jobID) == 0 {
		panic("jobID is empty")
	}

	return path.Join("/pingjob", agentID, jobID)
}
