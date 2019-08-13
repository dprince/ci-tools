package clusterinstall

const installTemplateE2E = `
kind: Template
apiVersion: template.openshift.io/v1

parameters:
- name: JOB_NAME_SAFE
  required: true
- name: JOB_NAME_HASH
  required: true
- name: NAMESPACE
  required: true
- name: IMAGE_FORMAT
- name: IMAGE_INSTALLER
  required: true
- name: IMAGE_TESTS
  required: true
- name: CLUSTER_TYPE
  required: true
- name: TEST_COMMAND
  required: true
- name: RELEASE_IMAGE_LATEST
  required: true
- name: BASE_DOMAIN

objects:

# We want the cluster to be able to access these images
- kind: RoleBinding
  apiVersion: authorization.openshift.io/v1
  metadata:
    name: ${JOB_NAME_SAFE}-image-puller
    namespace: ${NAMESPACE}
  roleRef:
    name: system:image-puller
  subjects:
  - kind: SystemGroup
    name: system:unauthenticated
  - kind: SystemGroup
    name: system:authenticated

# Give edit access to a known bot
- kind: RoleBinding
  apiVersion: authorization.openshift.io/v1
  metadata:
    name: ${JOB_NAME_SAFE}-namespace-editors
    namespace: ${NAMESPACE}
  roleRef:
    name: edit
  subjects:
  - kind: ServiceAccount
    namespace: ci
    name: ci-chat-bot

# The e2e pod spins up a cluster, runs e2e tests, and then cleans up the cluster.
- kind: Pod
  apiVersion: v1
  metadata:
    name: ${JOB_NAME_SAFE}
    namespace: ${NAMESPACE}
    annotations:
      # we want to gather the teardown logs no matter what
      ci-operator.openshift.io/wait-for-container-artifacts: teardown
      ci-operator.openshift.io/save-container-logs: "true"
      ci-operator.openshift.io/container-sub-tests: "lease,setup,test,teardown"
  spec:
    restartPolicy: Never
    activeDeadlineSeconds: 14400
    terminationGracePeriodSeconds: 900
    volumes:
    - name: artifacts
      emptyDir: {}
    - name: shared-tmp
      emptyDir: {}
    - name: cluster-profile
      secret:
        secretName: ${JOB_NAME_SAFE}-cluster-profile

    containers:

    - name: lease
      image: registry.svc.ci.openshift.org/ci/boskoscli:latest
      terminationMessagePolicy: FallbackToLogsOnError
      resources:
        requests:
          cpu: 10m
          memory: 10Mi
        limits:
          memory: 200Mi
      volumeMounts:
      - name: shared-tmp
        mountPath: /tmp/shared
      - name: cluster-profile
        mountPath: /tmp/cluster
      - name: artifacts
        mountPath: /tmp/artifacts
      env:
      - name: CLUSTER_TYPE
        value: ${CLUSTER_TYPE}
      - name: CLUSTER_NAME
        value: ${NAMESPACE}-${JOB_NAME_HASH}
      command:
      - /bin/bash
      - -c
      - |
        #!/bin/bash
        set -euo pipefail

        trap 'rc=$?; CHILDREN=$(jobs -p); if test -n "${CHILDREN}"; then kill ${CHILDREN} && wait; fi; if test "${rc}" -ne 0; then touch /tmp/shared/exit; fi; exit "${rc}"' EXIT

        # hack for bazel
        function boskosctl() {
          /app/boskos/cmd/cli/app.binary "${@}"
        }

        echo "[INFO] Acquiring a lease ..."
        resource="$( boskosctl --server-url http://boskos.ci --owner-name "${CLUSTER_NAME}" acquire --type "${CLUSTER_TYPE}-quota-slice" --state free --target-state leased --timeout 150m )"
        touch /tmp/shared/leased
        echo "[INFO] Lease acquired!"
        echo "[INFO] Leased resource: ${resource}"

        function release() {
            local resource_name; resource_name="$( jq .name --raw-output <<<"${resource}" )"
            echo "[INFO] Releasing the lease on resouce ${resource_name}..."
            boskosctl --server-url http://boskos.ci --owner-name "${CLUSTER_NAME}" release --name "${resource_name}" --target-state free
        }
        trap release EXIT

        echo "[INFO] Sending heartbeats to retain the lease..."
        boskosctl --server-url http://boskos.ci --owner-name "${CLUSTER_NAME}" heartbeat --resource "${resource}" &

        while true; do
          if [[ -f /tmp/shared/exit ]]; then
            echo "Another process exited" 2>&1
            exit 0
          fi

          sleep 15 & wait $!
        done

    # Once the cluster is up, executes shared tests
    - name: test
      image: ${IMAGE_TESTS}
      terminationMessagePolicy: FallbackToLogsOnError
      resources:
        requests:
          cpu: 1
          memory: 300Mi
        limits:
          memory: 3Gi
      volumeMounts:
      - name: shared-tmp
        mountPath: /tmp/shared
      - name: cluster-profile
        mountPath: /tmp/cluster
      - name: artifacts
        mountPath: /tmp/artifacts
      env:
      - name: AWS_SHARED_CREDENTIALS_FILE
        value: /tmp/cluster/.awscred
      - name: AZURE_AUTH_LOCATION
        value: /tmp/cluster/osServicePrincipal.json
      - name: ARTIFACT_DIR
        value: /tmp/artifacts
      - name: HOME
        value: /tmp/home
      - name: IMAGE_FORMAT
        value: ${IMAGE_FORMAT}
      - name: KUBECONFIG
        value: /tmp/artifacts/installer/auth/kubeconfig
      command:
      - /bin/bash
      - -c
      - |
        #!/bin/bash
        set -euo pipefail

        export PATH=/usr/libexec/origin:$PATH

        trap 'touch /tmp/shared/exit' EXIT
        trap 'kill $(jobs -p); exit 0' TERM

        mkdir -p "${HOME}"

        # wait for the API to come up
        while true; do
          if [[ -f /tmp/shared/exit ]]; then
            echo "Another process exited" 2>&1
            exit 1
          fi
          if [[ ! -f /tmp/shared/setup-success ]]; then
            sleep 15 & wait
            continue
          fi
          # don't let clients impact the global kubeconfig
          cp "${KUBECONFIG}" /tmp/admin.kubeconfig
          export KUBECONFIG=/tmp/admin.kubeconfig
          break
        done

        # if the cluster profile included an insights secret, install it to the cluster to
        # report support data from the support-operator
        if [[ -f /tmp/cluster/insights-live.yaml ]]; then
          oc create -f /tmp/cluster/insights-live.yaml || true
        fi

        # set up cloud-provider-specific env vars
        export KUBE_SSH_BASTION="$( oc --insecure-skip-tls-verify get node -l node-role.kubernetes.io/master -o 'jsonpath={.items[0].status.addresses[?(@.type=="ExternalIP")].address}' ):22"
        export KUBE_SSH_KEY_PATH=/tmp/cluster/ssh-privatekey
        if [[ "${CLUSTER_TYPE}" == "gcp" ]]; then
          export GOOGLE_APPLICATION_CREDENTIALS="/tmp/cluster/gce.json"
          export KUBE_SSH_USER=cloud-user
          mkdir -p ~/.ssh
          cp /tmp/cluster/ssh-privatekey ~/.ssh/google_compute_engine || true
          export PROVIDER_ARGS='-provider=gce -gce-zone=us-east1-c -gce-project=openshift-gce-devel-ci'
          export TEST_PROVIDER='{"type":"gce","zone":"us-east1-c","projectid":"openshift-gce-devel-ci"}'
        elif [[ "${CLUSTER_TYPE}" == "aws" ]]; then
          mkdir -p ~/.ssh
          cp /tmp/cluster/ssh-privatekey ~/.ssh/kube_aws_rsa || true
          export PROVIDER_ARGS="-provider=aws -gce-zone=us-east-1"
          # TODO: make openshift-tests auto-discover this from cluster config
          export TEST_PROVIDER='{"type":"aws","region":"us-east-1","zone":"us-east-1a","multizone":true,"multimaster":true}'
          export KUBE_SSH_USER=core
        elif [[ "${CLUSTER_TYPE}" == "azure4" ]]; then
          export TEST_PROVIDER='azure'
        fi

        mkdir -p /tmp/output
        cd /tmp/output

        function run-upgrade-tests() {
          openshift-tests run-upgrade "${TEST_SUITE}" --to-image "${RELEASE_IMAGE_LATEST}" \
            --options "${TEST_OPTIONS:-}" \
            --provider "${TEST_PROVIDER:-}" -o /tmp/artifacts/e2e.log --junit-dir /tmp/artifacts/junit
          exit 0
        }

        function run-tests() {
          openshift-tests run "${TEST_SUITE}" \
            --provider "${TEST_PROVIDER:-}" -o /tmp/artifacts/e2e.log --junit-dir /tmp/artifacts/junit
          exit 0
        }

        ${TEST_COMMAND}

    # Runs an install
    - name: setup
      image: ${IMAGE_INSTALLER}
      terminationMessagePolicy: FallbackToLogsOnError
      volumeMounts:
      - name: shared-tmp
        mountPath: /tmp
      - name: cluster-profile
        mountPath: /etc/openshift-installer
      - name: artifacts
        mountPath: /tmp/artifacts
      env:
      - name: TYPE
        value: ${CLUSTER_TYPE}
      - name: AWS_SHARED_CREDENTIALS_FILE
        value: /etc/openshift-installer/.awscred
      - name: AWS_REGION
        value: us-east-1
      - name: AZURE_AUTH_LOCATION
        value: /etc/openshift-installer/osServicePrincipal.json
      - name: AZURE_REGION
        value: centralus
      - name: CLUSTER_NAME
        value: ${NAMESPACE}-${JOB_NAME_HASH}
      - name: BASE_DOMAIN
        value: ${BASE_DOMAIN}
      - name: SSH_PRIV_KEY_PATH
        value: /etc/openshift-installer/ssh-privatekey
      - name: SSH_PUB_KEY_PATH
        value: /etc/openshift-installer/ssh-publickey
      - name: PULL_SECRET_PATH
        value: /etc/openshift-installer/pull-secret
      - name: OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE
        value: ${RELEASE_IMAGE_LATEST}
      - name: USER
        value: test
      - name: HOME
        value: /tmp
      - name: INSTALL_INITIAL_RELEASE
      - name: RELEASE_IMAGE_INITIAL
      command:
      - /bin/sh
      - -c
      - |
        #!/bin/sh
        trap 'rc=$?; if test "${rc}" -eq 0; then touch /tmp/setup-success; else touch /tmp/exit; fi; exit "${rc}"' EXIT
        trap 'CHILDREN=$(jobs -p); if test -n "${CHILDREN}"; then kill ${CHILDREN} && wait; fi' TERM

        while true; do
          if [[ -f /tmp/exit ]]; then
            echo "Another process exited" 2>&1
            exit 1
          fi
          if [[ -f /tmp/leased ]]; then
            echo "Lease acquired, installing..."
            break
          fi

          sleep 15 & wait
        done

        cp "$(command -v openshift-install)" /tmp
        mkdir /tmp/artifacts/installer

        if [[ -n "${INSTALL_INITIAL_RELEASE}" && -n "${RELEASE_IMAGE_INITIAL}" ]]; then
          echo "Installing from initial release ${RELEASE_IMAGE_INITIAL}"
          OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE="${RELEASE_IMAGE_INITIAL}"
        else
          echo "Installing from release ${RELEASE_IMAGE_LATEST}"
        fi

        export EXPIRATION_DATE=$(date -d '4 hours' --iso=minutes --utc)
        export SSH_PUB_KEY=$(cat "${SSH_PUB_KEY_PATH}")
        export PULL_SECRET=$(cat "${PULL_SECRET_PATH}")

        ## move private key to ~/.ssh/ so that installer can use it to gather logs on bootstrap failure
        mkdir -p ~/.ssh
        cp "${SSH_PRIV_KEY_PATH}" ~/.ssh/

        if [[ "${CLUSTER_TYPE}" == "aws" ]]; then
            cat > /tmp/artifacts/installer/install-config.yaml << EOF
        apiVersion: v1
        baseDomain: ${BASE_DOMAIN:-origin-ci-int-aws.dev.rhcloud.com}
        metadata:
          name: ${CLUSTER_NAME}
        controlPlane:
          name: master
          replicas: 3
          platform:
            aws:
              zones:
              - us-east-1a
              - us-east-1b
              - us-east-1c
        compute:
        - name: worker
          replicas: 3
          platform:
            aws:
              zones:
              - us-east-1a
              - us-east-1b
              - us-east-1c
        platform:
          aws:
            region:       ${AWS_REGION}
            userTags:
              expirationDate: ${EXPIRATION_DATE}
        pullSecret: >
          ${PULL_SECRET}
        sshKey: |
          ${SSH_PUB_KEY}
        EOF
        elif [[ "${CLUSTER_TYPE}" == "azure4" ]]; then
            cat > /tmp/artifacts/installer/install-config.yaml << EOF
        apiVersion: v1
        baseDomain: ${BASE_DOMAIN:-ci.azure.devcluster.openshift.com}
        metadata:
          name: ${CLUSTER_NAME}
        controlPlane:
          name: master
          replicas: 3
        compute:
        - name: worker
          replicas: 3
        platform:
          azure:
            baseDomainResourceGroupName: os4-common
            region: ${AZURE_REGION}
        pullSecret: >
          ${PULL_SECRET}
        sshKey: |
          ${SSH_PUB_KEY}
        EOF
        else
            echo "Unsupported cluster type '${CLUSTER_TYPE}'"
            exit 1
        fi

        TF_LOG=debug openshift-install --dir=/tmp/artifacts/installer create cluster &
        wait "$!"

    # Performs cleanup of all created resources
    - name: teardown
      image: ${IMAGE_TESTS}
      terminationMessagePolicy: FallbackToLogsOnError
      volumeMounts:
      - name: shared-tmp
        mountPath: /tmp/shared
      - name: cluster-profile
        mountPath: /etc/openshift-installer
      - name: artifacts
        mountPath: /tmp/artifacts
      env:
      - name: INSTANCE_PREFIX
        value: ${NAMESPACE}-${JOB_NAME_HASH}
      - name: TYPE
        value: ${CLUSTER_TYPE}
      - name: AWS_SHARED_CREDENTIALS_FILE
        value: /etc/openshift-installer/.awscred
      - name: AWS_REGION
        value: us-east-1
      - name: AZURE_AUTH_LOCATION
        value: /etc/openshift-installer/osServicePrincipal.json
      - name: AZURE_REGION
        value: centralus
      - name: KUBECONFIG
        value: /tmp/artifacts/installer/auth/kubeconfig
      command:
      - /bin/bash
      - -c
      - |
        #!/bin/bash
        function queue() {
          local TARGET="${1}"
          shift
          local LIVE="$(jobs | wc -l)"
          while [[ "${LIVE}" -ge 45 ]]; do
            sleep 1
            LIVE="$(jobs | wc -l)"
          done
          echo "${@}"
          if [[ -n "${FILTER}" ]]; then
            "${@}" | "${FILTER}" >"${TARGET}" &
          else
            "${@}" >"${TARGET}" &
          fi
        }

        function teardown() {
          set +e
          touch /tmp/shared/exit
          export PATH=$PATH:/tmp/shared

          echo "Gathering artifacts ..."
          mkdir -p /tmp/artifacts/pods /tmp/artifacts/nodes /tmp/artifacts/metrics /tmp/artifacts/bootstrap /tmp/artifacts/network

          oc --insecure-skip-tls-verify --request-timeout=5s get nodes -o jsonpath --template '{range .items[*]}{.metadata.name}{"\n"}{end}' > /tmp/nodes
          oc --insecure-skip-tls-verify --request-timeout=5s get pods --all-namespaces --template '{{ range .items }}{{ $name := .metadata.name }}{{ $ns := .metadata.namespace }}{{ range .spec.containers }}-n {{ $ns }} {{ $name }} -c {{ .name }}{{ "\n" }}{{ end }}{{ range .spec.initContainers }}-n {{ $ns }} {{ $name }} -c {{ .name }}{{ "\n" }}{{ end }}{{ end }}' > /tmp/containers
          oc --insecure-skip-tls-verify --request-timeout=5s get pods -l openshift.io/component=api --all-namespaces --template '{{ range .items }}-n {{ .metadata.namespace }} {{ .metadata.name }}{{ "\n" }}{{ end }}' > /tmp/pods-api

          queue /tmp/artifacts/config-resources.json oc --insecure-skip-tls-verify --request-timeout=5s get apiserver.config.openshift.io authentication.config.openshift.io build.config.openshift.io console.config.openshift.io dns.config.openshift.io featuregate.config.openshift.io image.config.openshift.io infrastructure.config.openshift.io ingress.config.openshift.io network.config.openshift.io oauth.config.openshift.io project.config.openshift.io scheduler.config.openshift.io -o json
          queue /tmp/artifacts/apiservices.json oc --insecure-skip-tls-verify --request-timeout=5s get apiservices -o json
          queue /tmp/artifacts/clusteroperators.json oc --insecure-skip-tls-verify --request-timeout=5s get clusteroperators -o json
          queue /tmp/artifacts/clusterversion.json oc --insecure-skip-tls-verify --request-timeout=5s get clusterversion -o json
          queue /tmp/artifacts/configmaps.json oc --insecure-skip-tls-verify --request-timeout=5s get configmaps --all-namespaces -o json
          queue /tmp/artifacts/credentialsrequests.json oc --insecure-skip-tls-verify --request-timeout=5s get credentialsrequests --all-namespaces -o json
          queue /tmp/artifacts/csr.json oc --insecure-skip-tls-verify --request-timeout=5s get csr -o json
          queue /tmp/artifacts/endpoints.json oc --insecure-skip-tls-verify --request-timeout=5s get endpoints --all-namespaces -o json
          FILTER=gzip queue /tmp/artifacts/deployments.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get deployments --all-namespaces -o json
          FILTER=gzip queue /tmp/artifacts/daemonsets.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get daemonsets --all-namespaces -o json
          queue /tmp/artifacts/events.json oc --insecure-skip-tls-verify --request-timeout=5s get events --all-namespaces -o json
          queue /tmp/artifacts/kubeapiserver.json oc --insecure-skip-tls-verify --request-timeout=5s get kubeapiserver -o json
          queue /tmp/artifacts/kubecontrollermanager.json oc --insecure-skip-tls-verify --request-timeout=5s get kubecontrollermanager -o json
          queue /tmp/artifacts/machineconfigpools.json oc --insecure-skip-tls-verify --request-timeout=5s get machineconfigpools -o json
          queue /tmp/artifacts/machineconfigs.json oc --insecure-skip-tls-verify --request-timeout=5s get machineconfigs -o json
          queue /tmp/artifacts/namespaces.json oc --insecure-skip-tls-verify --request-timeout=5s get namespaces -o json
          queue /tmp/artifacts/nodes.json oc --insecure-skip-tls-verify --request-timeout=5s get nodes -o json
          queue /tmp/artifacts/openshiftapiserver.json oc --insecure-skip-tls-verify --request-timeout=5s get openshiftapiserver -o json
          queue /tmp/artifacts/pods.json oc --insecure-skip-tls-verify --request-timeout=5s get pods --all-namespaces -o json
          queue /tmp/artifacts/persistentvolumes.json oc --insecure-skip-tls-verify --request-timeout=5s get persistentvolumes --all-namespaces -o json
          queue /tmp/artifacts/persistentvolumeclaims.json oc --insecure-skip-tls-verify --request-timeout=5s get persistentvolumeclaims --all-namespaces -o json
          FILTER=gzip queue /tmp/artifacts/replicasets.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get replicasets --all-namespaces -o json
          queue /tmp/artifacts/rolebindings.json oc --insecure-skip-tls-verify --request-timeout=5s get rolebindings --all-namespaces -o json
          queue /tmp/artifacts/roles.json oc --insecure-skip-tls-verify --request-timeout=5s get roles --all-namespaces -o json
          queue /tmp/artifacts/services.json oc --insecure-skip-tls-verify --request-timeout=5s get services --all-namespaces -o json
          FILTER=gzip queue /tmp/artifacts/statefulsets.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get statefulsets --all-namespaces -o json

          FILTER=gzip queue /tmp/artifacts/openapi.json.gz oc --insecure-skip-tls-verify --request-timeout=5s get --raw /openapi/v2

          # gather nodes first in parallel since they may contain the most relevant debugging info
          while IFS= read -r i; do
            mkdir -p /tmp/artifacts/nodes/$i
            queue /tmp/artifacts/nodes/$i/heap oc --insecure-skip-tls-verify get --request-timeout=20s --raw /api/v1/nodes/$i/proxy/debug/pprof/heap
          done < /tmp/nodes

          FILTER=gzip queue /tmp/artifacts/nodes/masters-journal.gz oc --insecure-skip-tls-verify adm node-logs --role=master --unify=false
          FILTER=gzip queue /tmp/artifacts/nodes/workers-journal.gz oc --insecure-skip-tls-verify adm node-logs --role=worker --unify=false

          # Snapshot iptables-save on each node for debugging possible kube-proxy issues
          oc --insecure-skip-tls-verify get --request-timeout=20s -n openshift-sdn -l app=sdn pods --template '{{ range .items }}{{ .metadata.name }}{{ "\n" }}{{ end }}' > /tmp/sdn-pods
          while IFS= read -r i; do
            queue /tmp/artifacts/network/iptables-save-$i oc --insecure-skip-tls-verify rsh --timeout=20 -n openshift-sdn -c sdn $i iptables-save -c
          done < /tmp/sdn-pods

          while IFS= read -r i; do
            file="$( echo "$i" | cut -d ' ' -f 3 | tr -s ' ' '_' )"
            queue /tmp/artifacts/metrics/${file}-heap oc --insecure-skip-tls-verify exec $i -- /bin/bash -c 'oc --insecure-skip-tls-verify get --raw /debug/pprof/heap --server "https://$( hostname ):8443" --config /etc/origin/master/admin.kubeconfig'
            queue /tmp/artifacts/metrics/${file}-controllers-heap oc --insecure-skip-tls-verify exec $i -- /bin/bash -c 'oc --insecure-skip-tls-verify get --raw /debug/pprof/heap --server "https://$( hostname ):8444" --config /etc/origin/master/admin.kubeconfig'
          done < /tmp/pods-api

          while IFS= read -r i; do
            file="$( echo "$i" | cut -d ' ' -f 2,3,5 | tr -s ' ' '_' )"
            FILTER=gzip queue /tmp/artifacts/pods/${file}.log.gz oc --insecure-skip-tls-verify logs --request-timeout=20s $i
            FILTER=gzip queue /tmp/artifacts/pods/${file}_previous.log.gz oc --insecure-skip-tls-verify logs --request-timeout=20s -p $i
          done < /tmp/containers

          echo "Snapshotting prometheus (may take 15s) ..."
          queue /tmp/artifacts/metrics/prometheus.tar.gz oc --insecure-skip-tls-verify exec -n openshift-monitoring prometheus-k8s-0 -- tar cvzf - -C /prometheus .

          echo "Running must-gather..."
          mkdir -p /tmp/artifacts/must-gather
          queue /tmp/artifacts/must-gather/must-gather.log oc --insecure-skip-tls-verify adm must-gather --dest-dir /tmp/artifacts/must-gather

          echo "Waiting for logs ..."
          wait

          echo "Deprovisioning cluster ..."
          openshift-install --dir /tmp/artifacts/installer destroy cluster
        }

        trap 'teardown' EXIT
        trap 'kill $(jobs -p); exit 0' TERM

        for i in $(seq 1 180); do
          if [[ -f /tmp/shared/exit ]]; then
            exit 0
          fi
          sleep 60 & wait
        done
`
