package cluster

/*
// TestNomadExecCluster tests a three node exec Nomad cluster talking to a
// three node exec Consul cluster.
func TestNomadExecCluster(t *testing.T) {
	var consulTargets, nomadTargets []string
	for _, c := range consulCluster.servers {
		ccfg, err := c.APIConfig()
		if err != nil {
			t.Fatal(err)
		}
		consulTargets = append(consulTargets, fmt.Sprintf("'%s'", ccfg.Address.Host))
	}

	for _, c := range cluster.servers {
		ncfg, err := c.APIConfig()
		if err != nil {
			t.Fatal(err)
		}
		nomadTargets = append(nomadTargets, fmt.Sprintf("'%s'", ncfg.Address.Host))
	}

	pcfg := fmt.Sprintf(`
- job_name: consul-servers
  metrics_path: /v1/agent/metrics
  params:
    format:
    - prometheus
  static_configs:
  - targets: [%s]
  # See https://github.com/hashicorp/consul/issues/4450
  metric_relabel_configs:
  - source_labels: [__name__]
    regex: 'consul_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat|installSnapshot)_((\w){36})((_sum)|(_count))?'
    target_label: raft_id
    replacement: '${2}'
  - source_labels: [__name__]
    regex: 'consul_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat|installSnapshot)_((\w){36})((_sum)|(_count))?'
    target_label: __name__
    replacement: 'consul_raft_replication_${1}${4}'

- job_name: nomad-servers
  metrics_path: /v1/metrics
  params:
    format:
    - prometheus
  static_configs:
  - targets: [%s]
  metric_relabel_configs:
  - source_labels: [__name__]
    regex: 'nomad_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat)_([^:]+:\d+)(_sum|_count)?'
    target_label: peer_instance
    replacement: '${2}'
  - source_labels: [__name__]
    regex: 'nomad_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat)_([^:]+:\d+)(_sum|_count)?'
    target_label: __name__
    replacement: 'nomad_raft_replication_${1}${3}'

`, strings.Join(consulTargets, ", "), strings.Join(nomadTargets, ", "))

	testJobs(t, te.Ctx, consul, nomad, fmt.Sprintf(execJobHCL, pcfg, te.PrometheusPath))
}

*/
