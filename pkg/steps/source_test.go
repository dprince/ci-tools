package steps

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/test-infra/prow/apis/prowjobs/v1"
	"k8s.io/test-infra/prow/pod-utils/downwardapi"
	"testing"

	coreapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/diff"

	buildapi "github.com/openshift/api/build/v1"

	"github.com/openshift/ci-tools/pkg/api"
)

func strP(str string) *string {
	return &str
}

func TestCreateBuild(t *testing.T) {
	layer := buildapi.ImageOptimizationSkipLayers
	var testCases = []struct {
		name         string
		config       api.SourceStepConfiguration
		jobSpec      *api.JobSpec
		clonerefsRef coreapi.ObjectReference
		resources    api.ResourceConfiguration
		expected     *buildapi.Build
	}{
		{
			name: "basic options for a presubmit",
			config: api.SourceStepConfiguration{
				From: api.PipelineImageStreamTagReferenceRoot,
				To:   api.PipelineImageStreamTagReferenceSource,
				ClonerefsImage: api.ImageStreamTagReference{
					Cluster:   "https://api.ci.openshift.org",
					Namespace: "ci",
					Name:      "clonerefs",
					Tag:       "latest",
				},
				ClonerefsPath: "/app/prow/cmd/clonerefs/app.binary.runfiles/io_k8s_test_infra/prow/cmd/clonerefs/linux_amd64_pure_stripped/app.binary",
			},
			jobSpec: &api.JobSpec{
				JobSpec: downwardapi.JobSpec{
					Job:       "job",
					BuildID:   "buildId",
					ProwJobID: "prowJobId",
					Refs: &v1.Refs{
						Org:     "org",
						Repo:    "repo",
						BaseRef: "master",
						BaseSHA: "masterSHA",
						Pulls: []v1.Pull{{
							Number: 1,
							SHA:    "pullSHA",
						}},
					},
				},
				Namespace: "namespace",
			},
			clonerefsRef: coreapi.ObjectReference{Kind: "ImageStreamTag", Name: "clonerefs:latest", Namespace: "ci"},
			resources:    map[string]api.ResourceRequirements{"*": {Requests: map[string]string{"cpu": "200m"}}},

			expected: &buildapi.Build{
				ObjectMeta: meta.ObjectMeta{
					Name:      "src",
					Namespace: "namespace",
					Labels: map[string]string{
						"persists-between-builds": "false",
						"job":                     "job",
						"build-id":                "buildId",
						"prow.k8s.io/id":          "prowJobId",
						"creates":                 "src",
						"created-by-ci":           "true",
					},
					Annotations: map[string]string{
						"ci.openshift.io/job-spec": ``, // set via unexported fields so will be empty
					},
				},
				Spec: buildapi.BuildSpec{
					CommonSpec: buildapi.CommonSpec{
						Resources:      coreapi.ResourceRequirements{Requests: map[coreapi.ResourceName]resource.Quantity{"cpu": resource.MustParse("200m")}},
						ServiceAccount: "builder",
						Source: buildapi.BuildSource{
							Type: buildapi.BuildSourceDockerfile,
							Dockerfile: strP(`
FROM pipeline:root
ADD ./app.binary /clonerefs
RUN umask 0002 && /clonerefs && find /go/src -type d -not -perm -0775 | xargs -r chmod g+xw
WORKDIR /go/src/github.com/org/repo/
ENV GOPATH=/go
RUN git submodule update --init
`),
							Images: []buildapi.ImageSource{
								{
									From: coreapi.ObjectReference{Kind: "ImageStreamTag", Name: "clonerefs:latest", Namespace: "ci"},
									Paths: []buildapi.ImageSourcePath{
										{
											SourcePath:     "/app/prow/cmd/clonerefs/app.binary.runfiles/io_k8s_test_infra/prow/cmd/clonerefs/linux_amd64_pure_stripped/app.binary",
											DestinationDir: ".",
										},
									},
								},
							},
						},
						Strategy: buildapi.BuildStrategy{
							Type: buildapi.DockerBuildStrategyType,
							DockerStrategy: &buildapi.DockerBuildStrategy{
								DockerfilePath:          "",
								From:                    &coreapi.ObjectReference{Kind: "ImageStreamTag", Namespace: "namespace", Name: "pipeline:root"},
								ForcePull:               true,
								NoCache:                 true,
								Env:                     []coreapi.EnvVar{{Name: "CLONEREFS_OPTIONS", Value: `{"src_root":"/go","log":"/dev/null","git_user_name":"ci-robot","git_user_email":"ci-robot@openshift.io","refs":[{"org":"org","repo":"repo","base_ref":"master","base_sha":"masterSHA","pulls":[{"number":1,"author":"","sha":"pullSHA"}]}]}`}},
								ImageOptimizationPolicy: &layer,
							},
						},
						Output: buildapi.BuildOutput{
							To: &coreapi.ObjectReference{
								Kind:      "ImageStreamTag",
								Namespace: "namespace",
								Name:      "pipeline:src",
							},
						},
					},
				},
			},
		},
		{
			name: "with a path alias",
			config: api.SourceStepConfiguration{
				From: api.PipelineImageStreamTagReferenceRoot,
				To:   api.PipelineImageStreamTagReferenceSource,
				ClonerefsImage: api.ImageStreamTagReference{
					Cluster:   "https://api.ci.openshift.org",
					Namespace: "ci",
					Name:      "clonerefs",
					Tag:       "latest",
				},
				ClonerefsPath: "/app/prow/cmd/clonerefs/app.binary.runfiles/io_k8s_test_infra/prow/cmd/clonerefs/linux_amd64_pure_stripped/app.binary",
			},
			jobSpec: &api.JobSpec{
				JobSpec: downwardapi.JobSpec{
					Job:       "job",
					BuildID:   "buildId",
					ProwJobID: "prowJobId",
					Refs: &v1.Refs{
						Org:       "org",
						Repo:      "repo",
						BaseRef:   "master",
						BaseSHA:   "masterSHA",
						PathAlias: "somewhere/else",
						Pulls: []v1.Pull{{
							Number: 1,
							SHA:    "pullSHA",
						}},
					},
				},
				Namespace: "namespace",
			},
			clonerefsRef: coreapi.ObjectReference{Kind: "ImageStreamTag", Name: "clonerefs:latest", Namespace: "ci"},
			resources:    map[string]api.ResourceRequirements{"*": {Requests: map[string]string{"cpu": "200m"}}},

			expected: &buildapi.Build{
				ObjectMeta: meta.ObjectMeta{
					Name:      "src",
					Namespace: "namespace",
					Labels: map[string]string{
						"persists-between-builds": "false",
						"job":                     "job",
						"build-id":                "buildId",
						"prow.k8s.io/id":          "prowJobId",
						"creates":                 "src",
						"created-by-ci":           "true",
					},
					Annotations: map[string]string{
						"ci.openshift.io/job-spec": ``, // set via unexported fields so will be empty
					},
				},
				Spec: buildapi.BuildSpec{
					CommonSpec: buildapi.CommonSpec{
						Resources:      coreapi.ResourceRequirements{Requests: map[coreapi.ResourceName]resource.Quantity{"cpu": resource.MustParse("200m")}},
						ServiceAccount: "builder",
						Source: buildapi.BuildSource{
							Type: buildapi.BuildSourceDockerfile,
							Dockerfile: strP(`
FROM pipeline:root
ADD ./app.binary /clonerefs
RUN umask 0002 && /clonerefs && find /go/src -type d -not -perm -0775 | xargs -r chmod g+xw
WORKDIR /go/src/somewhere/else/
ENV GOPATH=/go
RUN git submodule update --init
`),
							Images: []buildapi.ImageSource{
								{
									From: coreapi.ObjectReference{Kind: "ImageStreamTag", Name: "clonerefs:latest", Namespace: "ci"},
									Paths: []buildapi.ImageSourcePath{
										{
											SourcePath:     "/app/prow/cmd/clonerefs/app.binary.runfiles/io_k8s_test_infra/prow/cmd/clonerefs/linux_amd64_pure_stripped/app.binary",
											DestinationDir: ".",
										},
									},
								},
							},
						},
						Strategy: buildapi.BuildStrategy{
							Type: buildapi.DockerBuildStrategyType,
							DockerStrategy: &buildapi.DockerBuildStrategy{
								DockerfilePath:          "",
								From:                    &coreapi.ObjectReference{Kind: "ImageStreamTag", Namespace: "namespace", Name: "pipeline:root"},
								ForcePull:               true,
								NoCache:                 true,
								Env:                     []coreapi.EnvVar{{Name: "CLONEREFS_OPTIONS", Value: `{"src_root":"/go","log":"/dev/null","git_user_name":"ci-robot","git_user_email":"ci-robot@openshift.io","refs":[{"org":"org","repo":"repo","base_ref":"master","base_sha":"masterSHA","pulls":[{"number":1,"author":"","sha":"pullSHA"}],"path_alias":"somewhere/else"}]}`}},
								ImageOptimizationPolicy: &layer,
							},
						},
						Output: buildapi.BuildOutput{
							To: &coreapi.ObjectReference{
								Kind:      "ImageStreamTag",
								Namespace: "namespace",
								Name:      "pipeline:src",
							},
						},
					},
				},
			},
		},
		{
			name: "with extra refs",
			config: api.SourceStepConfiguration{
				From: api.PipelineImageStreamTagReferenceRoot,
				To:   api.PipelineImageStreamTagReferenceSource,
				ClonerefsImage: api.ImageStreamTagReference{
					Cluster:   "https://api.ci.openshift.org",
					Namespace: "ci",
					Name:      "clonerefs",
					Tag:       "latest",
				},
				ClonerefsPath: "/app/prow/cmd/clonerefs/app.binary.runfiles/io_k8s_test_infra/prow/cmd/clonerefs/linux_amd64_pure_stripped/app.binary",
			},
			jobSpec: &api.JobSpec{
				JobSpec: downwardapi.JobSpec{
					Job:       "job",
					BuildID:   "buildId",
					ProwJobID: "prowJobId",
					Refs: &v1.Refs{
						Org:     "org",
						Repo:    "repo",
						BaseRef: "master",
						BaseSHA: "masterSHA",
						Pulls: []v1.Pull{{
							Number: 1,
							SHA:    "pullSHA",
						}},
					},
					ExtraRefs: []v1.Refs{{
						Org:     "org",
						Repo:    "other",
						BaseRef: "master",
						BaseSHA: "masterSHA",
					}},
				},
				Namespace: "namespace",
			},
			clonerefsRef: coreapi.ObjectReference{Kind: "ImageStreamTag", Name: "clonerefs:latest", Namespace: "ci"},
			resources:    map[string]api.ResourceRequirements{"*": {Requests: map[string]string{"cpu": "200m"}}},

			expected: &buildapi.Build{
				ObjectMeta: meta.ObjectMeta{
					Name:      "src",
					Namespace: "namespace",
					Labels: map[string]string{
						"persists-between-builds": "false",
						"job":                     "job",
						"build-id":                "buildId",
						"prow.k8s.io/id":          "prowJobId",
						"creates":                 "src",
						"created-by-ci":           "true",
					},
					Annotations: map[string]string{
						"ci.openshift.io/job-spec": ``, // set via unexported fields so will be empty
					},
				},
				Spec: buildapi.BuildSpec{
					CommonSpec: buildapi.CommonSpec{
						Resources:      coreapi.ResourceRequirements{Requests: map[coreapi.ResourceName]resource.Quantity{"cpu": resource.MustParse("200m")}},
						ServiceAccount: "builder",
						Source: buildapi.BuildSource{
							Type: buildapi.BuildSourceDockerfile,
							Dockerfile: strP(`
FROM pipeline:root
ADD ./app.binary /clonerefs
RUN umask 0002 && /clonerefs && find /go/src -type d -not -perm -0775 | xargs -r chmod g+xw
WORKDIR /go/src/github.com/org/repo/
ENV GOPATH=/go
RUN git submodule update --init
`),
							Images: []buildapi.ImageSource{
								{
									From: coreapi.ObjectReference{Kind: "ImageStreamTag", Name: "clonerefs:latest", Namespace: "ci"},
									Paths: []buildapi.ImageSourcePath{
										{
											SourcePath:     "/app/prow/cmd/clonerefs/app.binary.runfiles/io_k8s_test_infra/prow/cmd/clonerefs/linux_amd64_pure_stripped/app.binary",
											DestinationDir: ".",
										},
									},
								},
							},
						},
						Strategy: buildapi.BuildStrategy{
							Type: buildapi.DockerBuildStrategyType,
							DockerStrategy: &buildapi.DockerBuildStrategy{
								DockerfilePath:          "",
								From:                    &coreapi.ObjectReference{Kind: "ImageStreamTag", Namespace: "namespace", Name: "pipeline:root"},
								ForcePull:               true,
								NoCache:                 true,
								Env:                     []coreapi.EnvVar{{Name: "CLONEREFS_OPTIONS", Value: `{"src_root":"/go","log":"/dev/null","git_user_name":"ci-robot","git_user_email":"ci-robot@openshift.io","refs":[{"org":"org","repo":"repo","base_ref":"master","base_sha":"masterSHA","pulls":[{"number":1,"author":"","sha":"pullSHA"}]},{"org":"org","repo":"other","base_ref":"master","base_sha":"masterSHA"}]}`}},
								ImageOptimizationPolicy: &layer,
							},
						},
						Output: buildapi.BuildOutput{
							To: &coreapi.ObjectReference{
								Kind:      "ImageStreamTag",
								Namespace: "namespace",
								Name:      "pipeline:src",
							},
						},
					},
				},
			},
		},
		{
			name: "with extra refs setting workdir and path alias",
			config: api.SourceStepConfiguration{
				From: api.PipelineImageStreamTagReferenceRoot,
				To:   api.PipelineImageStreamTagReferenceSource,
				ClonerefsImage: api.ImageStreamTagReference{
					Cluster:   "https://api.ci.openshift.org",
					Namespace: "ci",
					Name:      "clonerefs",
					Tag:       "latest",
				},
				ClonerefsPath: "/app/prow/cmd/clonerefs/app.binary.runfiles/io_k8s_test_infra/prow/cmd/clonerefs/linux_amd64_pure_stripped/app.binary",
			},
			jobSpec: &api.JobSpec{
				JobSpec: downwardapi.JobSpec{
					Job:       "job",
					BuildID:   "buildId",
					ProwJobID: "prowJobId",
					Refs: &v1.Refs{
						Org:     "org",
						Repo:    "repo",
						BaseRef: "master",
						BaseSHA: "masterSHA",
						Pulls: []v1.Pull{{
							Number: 1,
							SHA:    "pullSHA",
						}},
					},
					ExtraRefs: []v1.Refs{{
						Org:       "org",
						Repo:      "other",
						BaseRef:   "master",
						BaseSHA:   "masterSHA",
						WorkDir:   true,
						PathAlias: "this/is/nuts",
					}},
				},
				Namespace: "namespace",
			},
			clonerefsRef: coreapi.ObjectReference{Kind: "ImageStreamTag", Name: "clonerefs:latest", Namespace: "ci"},
			resources:    map[string]api.ResourceRequirements{"*": {Requests: map[string]string{"cpu": "200m"}}},

			expected: &buildapi.Build{
				ObjectMeta: meta.ObjectMeta{
					Name:      "src",
					Namespace: "namespace",
					Labels: map[string]string{
						"persists-between-builds": "false",
						"job":                     "job",
						"build-id":                "buildId",
						"prow.k8s.io/id":          "prowJobId",
						"creates":                 "src",
						"created-by-ci":           "true",
					},
					Annotations: map[string]string{
						"ci.openshift.io/job-spec": ``, // set via unexported fields so will be empty
					},
				},
				Spec: buildapi.BuildSpec{
					CommonSpec: buildapi.CommonSpec{
						Resources:      coreapi.ResourceRequirements{Requests: map[coreapi.ResourceName]resource.Quantity{"cpu": resource.MustParse("200m")}},
						ServiceAccount: "builder",
						Source: buildapi.BuildSource{
							Type: buildapi.BuildSourceDockerfile,
							Dockerfile: strP(`
FROM pipeline:root
ADD ./app.binary /clonerefs
RUN umask 0002 && /clonerefs && find /go/src -type d -not -perm -0775 | xargs -r chmod g+xw
WORKDIR /go/src/this/is/nuts/
ENV GOPATH=/go
RUN git submodule update --init
`),
							Images: []buildapi.ImageSource{
								{
									From: coreapi.ObjectReference{Kind: "ImageStreamTag", Name: "clonerefs:latest", Namespace: "ci"},
									Paths: []buildapi.ImageSourcePath{
										{
											SourcePath:     "/app/prow/cmd/clonerefs/app.binary.runfiles/io_k8s_test_infra/prow/cmd/clonerefs/linux_amd64_pure_stripped/app.binary",
											DestinationDir: ".",
										},
									},
								},
							},
						},
						Strategy: buildapi.BuildStrategy{
							Type: buildapi.DockerBuildStrategyType,
							DockerStrategy: &buildapi.DockerBuildStrategy{
								DockerfilePath:          "",
								From:                    &coreapi.ObjectReference{Kind: "ImageStreamTag", Namespace: "namespace", Name: "pipeline:root"},
								ForcePull:               true,
								NoCache:                 true,
								Env:                     []coreapi.EnvVar{{Name: "CLONEREFS_OPTIONS", Value: `{"src_root":"/go","log":"/dev/null","git_user_name":"ci-robot","git_user_email":"ci-robot@openshift.io","refs":[{"org":"org","repo":"repo","base_ref":"master","base_sha":"masterSHA","pulls":[{"number":1,"author":"","sha":"pullSHA"}]},{"org":"org","repo":"other","base_ref":"master","base_sha":"masterSHA","path_alias":"this/is/nuts","workdir":true}]}`}},
								ImageOptimizationPolicy: &layer,
							},
						},
						Output: buildapi.BuildOutput{
							To: &coreapi.ObjectReference{
								Kind:      "ImageStreamTag",
								Namespace: "namespace",
								Name:      "pipeline:src",
							},
						},
					},
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if actual, expected := createBuild(testCase.config, testCase.jobSpec, testCase.clonerefsRef, testCase.resources), testCase.expected; !equality.Semantic.DeepEqual(actual, expected) {
				t.Errorf("%s: got incorrect build: %v", testCase.name, diff.ObjectReflectDiff(actual, expected))
			}
		})
	}
}
