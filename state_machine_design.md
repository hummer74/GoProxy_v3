// state_machine_design.md
# Connection Failover and Recovery State Machine Design

## States
1.  **Normal**: All hosts in the chain are reachable and operational. Tunnel is active on the primary/configured path.
2.  **Failover**: A host in the chain has failed, or the main tunnel connection is lost. The system is actively attempting to switch to the fastest available healthy host.
3.  **Recovery**: All previously failed hosts have been restored and verified as operational. The system is re-establishing the original chain configuration.
4.  **Error/Halt**: A critical, unrecoverable error has occurred (e.g., SSH key failure, configuration corruption). System halts connection attempts until manual intervention.

## Transitions

| Current State | Trigger Event | Next State | Action Taken |
| :--- | :--- | :--- | :--- |
| **Normal** | Host Failure Detected (from Monitor) | Failover | 1. Identify fastest available host using latency metrics. 2. Attempt connection to the new host. 3. Update `ConnectionState` to Failover state. |
| **Failover** | Fastest Host Connection Successful | Recovery | 1. Verify all hosts in the chain are now reachable. 2. Re-establish original chain configuration. 3. Update `ConnectionState` to Normal. |
| **Failover** | Fastest Host Connection Fails | Failover (Self-Loop/Retry) | 1. Try the next fastest host. 2. If all fail, transition to Error/Halt. |
| **Recovery** | All Hosts Verified Operational | Normal | 1. Revert connection configuration to the original chain defined in `ConnectOptions`. 2. Update `ConnectionState` to Normal. |
| **Any State** | Critical Error Detected (e.g., SSH key error) | Error/Halt | Log critical error. Stop all tunnel operations. |

## Key Logic Points
*   **Fastest Host Selection:** Implemented via `MockGetLatency` (or real latency probe).
*   **Return-to-Original Chain:** Handled in the Recovery state by re-applying the original `ConnectOptions.Hosts`.
*   **State Persistence:** All state changes must be persisted to `ConnectionState` for recovery.