# Virtual clusters

If we want to test a change to Yurt, or an upgrade to a newer version of 
Consul/Nomad/etc, it's time-consuming to deploy via OS re-image.  

If we want to reap all the benefits of immutable infrastructure we shouldn't
have to support the use case of "upgrade", so we don't want to try to modify
a running cluster node in-place to test changes.

Virtual clusters are used in the Go tests, but they can be useful for manual
testing as well, or for defining Prometheus alerts experimentally.

To create a 3-node Consul/Nomad cluster:

```
yurt-cluster 
```

This will create virtual nodes as subprocesses, all listening on different ports
on localhost.  You can now submit use their UIs and submit Nomad jobs, which 
will run on the local machine.  Use `ps` to find a server process and kill it,
see what happens.

5-node cluster with TLS:

```
yurt-cluster -nodes=5 -tls
```

