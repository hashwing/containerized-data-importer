package controller

import (
	"reflect"
	"strings"
	"testing"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/pkg/apis/volumesnapshot/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	bootstrapapi "k8s.io/cluster-bootstrap/token/api"

	crdv1 "github.com/kubernetes-csi/external-snapshotter/pkg/apis/volumesnapshot/v1alpha1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	cdiv1 "kubevirt.io/containerized-data-importer/pkg/apis/core/v1alpha1"
	cdifake "kubevirt.io/containerized-data-importer/pkg/client/clientset/versioned/fake"
	"kubevirt.io/containerized-data-importer/pkg/common"
	. "kubevirt.io/containerized-data-importer/pkg/common"
	"kubevirt.io/containerized-data-importer/pkg/token"
	"kubevirt.io/containerized-data-importer/pkg/util/cert"
)

func Test_checkPVC(t *testing.T) {
	//Create base pvcs and secrets
	pvcNoAnno := createPvc("testPvcNoAnno", "default", nil, nil)
	pvcWithEndPointAnno := createPvc("testPvcWithEndPointAnno", "default", map[string]string{AnnEndpoint: "http://test"}, nil)

	tests := []struct {
		name   string
		wantOk bool
		pvc    *v1.PersistentVolumeClaim
	}{
		{
			name:   "pvc does not have endpoint annotation or pod annotation",
			wantOk: false,
			pvc:    pvcNoAnno,
		},
		{
			name:   "pvc does have endpoint annotation and does not have a pod annotation",
			wantOk: true,
			pvc:    pvcWithEndPointAnno,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOk := checkPVC(tt.pvc, AnnEndpoint)
			if gotOk != tt.wantOk {
				t.Errorf("checkPVC() gotOk = %v, want %v", gotOk, tt.wantOk)
			}
		})
	}

}

func Test_checkClonePVC(t *testing.T) {
	//Create base pvcs and secrets
	pvcNoAnno := createPvc("testPvcNoAnno", "default", nil, nil)
	pvcWithCloneRequestAnno := createPvc("testPvcWithCloneRequestAnno", "default", map[string]string{AnnCloneRequest: "source-ns/golden-pvc"}, nil)
	tests := []struct {
		name   string
		wantOk bool
		pvc    *v1.PersistentVolumeClaim
	}{
		{
			name:   "pvc does not have cloneRequest annotation or pod annotation",
			wantOk: false,
			pvc:    pvcNoAnno,
		},
		{
			name:   "pvc does have CloneRequest annotation and does not have a pod annotation",
			wantOk: true,
			pvc:    pvcWithCloneRequestAnno,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOk := checkPVC(tt.pvc, AnnCloneRequest)
			if gotOk != tt.wantOk {
				t.Errorf("checkPVC() gotOk = %v, want %v", gotOk, tt.wantOk)
			}
		})
	}

}

func Test_getEndpoint(t *testing.T) {
	type args struct {
		pvc *v1.PersistentVolumeClaim
	}
	pvcNoAnno := createPvc("testPVCNoAnno", "default", nil, nil)
	pvcWithAnno := createPvc("testPVCWithAnno", "default", map[string]string{AnnEndpoint: "http://test"}, nil)
	pvcNoValue := createPvc("testPVCNoValue", "default", map[string]string{AnnEndpoint: ""}, nil)

	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name:    "expected to find endpoint annotation",
			args:    args{pvcWithAnno},
			want:    "http://test",
			wantErr: false,
		},
		{
			name:    "expected to not find endpoint annotation",
			args:    args{pvcNoAnno},
			want:    "",
			wantErr: true,
		},
		{
			name:    "missing endpoint value",
			args:    args{pvcNoValue},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := getEndpoint(tt.args.pvc)
			if got != tt.want {
				t.Errorf("getEndpoint() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getSource(t *testing.T) {
	type args struct {
		pvc *v1.PersistentVolumeClaim
	}

	pvcNoAnno := createPvc("testPVCNoAnno", "default", nil, nil)
	pvcNoneAnno := createPvc("testPVCNoneAnno", "default", map[string]string{AnnSource: SourceNone}, nil)
	pvcGlanceAnno := createPvc("testPVCNoneAnno", "default", map[string]string{AnnSource: SourceGlance}, nil)
	pvcInvalidValue := createPvc("testPVCInvalidValue", "default", map[string]string{AnnSource: "iaminvalid"}, nil)
	pvcRegistryAnno := createPvc("testPVCRegistryAnno", "default", map[string]string{AnnSource: SourceRegistry}, nil)

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "expected to find none anno",
			args: args{pvcNoneAnno},
			want: SourceNone,
		},
		{
			name: "expected to find http with invalid value",
			args: args{pvcInvalidValue},
			want: SourceHTTP,
		},
		{
			name: "expected to find http with no anno",
			args: args{pvcNoAnno},
			want: SourceHTTP,
		},
		{
			name: "expected to find glance with glance anno",
			args: args{pvcGlanceAnno},
			want: SourceGlance,
		},
		{
			name: "expected to find registry with registry anno",
			args: args{pvcRegistryAnno},
			want: SourceRegistry,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getSource(tt.args.pvc)
			if got != tt.want {
				t.Errorf("getSource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getVolumeMode(t *testing.T) {
	type args struct {
		pvc *v1.PersistentVolumeClaim
	}

	pvcVolumeModeBlock := createBlockPvc("testPVCVolumeModeBlock", "default", map[string]string{AnnSource: SourceHTTP}, nil)
	pvcVolumeModeFilesystem := createPvc("testPVCVolumeModeFS", "default", map[string]string{AnnSource: SourceHTTP}, nil)
	pvcVolumeModeFilesystemDefault := createPvc("testPVCVolumeModeFS", "default", map[string]string{AnnSource: SourceHTTP}, nil)

	tests := []struct {
		name string
		args args
		want corev1.PersistentVolumeMode
	}{
		{
			name: "expected volumeMode to be Block",
			args: args{pvcVolumeModeBlock},
			want: corev1.PersistentVolumeBlock,
		},
		{
			name: "expected volumeMode to be Filesystem",
			args: args{pvcVolumeModeFilesystem},
			want: corev1.PersistentVolumeFilesystem,
		},
		{
			name: "expected volumeMode to be Filesystem",
			args: args{pvcVolumeModeFilesystemDefault},
			want: corev1.PersistentVolumeFilesystem,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getVolumeMode(tt.args.pvc)
			if got != tt.want {
				t.Errorf("getVolumeMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getContentType(t *testing.T) {
	type args struct {
		pvc *v1.PersistentVolumeClaim
	}

	pvcNoAnno := createPvc("testPVCNoAnno", "default", nil, nil)
	pvcArchiveAnno := createPvc("testPVCArchiveAnno", "default", map[string]string{AnnContentType: string(cdiv1.DataVolumeArchive)}, nil)
	pvcKubevirtAnno := createPvc("testPVCKubevirtAnno", "default", map[string]string{AnnContentType: string(cdiv1.DataVolumeKubeVirt)}, nil)
	pvcInvalidValue := createPvc("testPVCInvalidValue", "default", map[string]string{AnnContentType: "iaminvalid"}, nil)

	tests := []struct {
		name string
		args args
		want string
	}{
		{
			name: "expected to kubevirt content type",
			args: args{pvcNoAnno},
			want: string(cdiv1.DataVolumeKubeVirt),
		},
		{
			name: "expected to find archive content type",
			args: args{pvcArchiveAnno},
			want: string(cdiv1.DataVolumeArchive),
		},
		{
			name: "expected to kubevirt content type",
			args: args{pvcKubevirtAnno},
			want: string(cdiv1.DataVolumeKubeVirt),
		},
		{
			name: "expected to find kubevirt with invalid anno",
			args: args{pvcInvalidValue},
			want: string(cdiv1.DataVolumeKubeVirt),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getContentType(tt.args.pvc)
			if got != tt.want {
				t.Errorf("getSource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getImageSize(t *testing.T) {
	type args struct {
		pvc *v1.PersistentVolumeClaim
	}

	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name:    "expected to get size 1G",
			args:    args{createPvc("testPVC", "default", nil, nil)},
			want:    "1G",
			wantErr: false,
		},
		{
			name:    "expected to get error, because of missing size",
			args:    args{createPvcNoSize("testPVC", "default", nil, nil)},
			want:    "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getRequestedImageSize(tt.args.pvc)
			if err != nil && !tt.wantErr {
				t.Errorf("Error retrieving adjusted image size, when not expecting error: %s", err.Error())
			}
			if got != tt.want {
				t.Errorf("getSource() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getSecretName(t *testing.T) {
	type args struct {
		client kubernetes.Interface
		pvc    *v1.PersistentVolumeClaim
	}
	//Create base pvcs and secrets
	pvcNoAnno := createPvc("testPVCNoAnno", "default", nil, nil)
	pvcWithAnno := createPvc("testPVCWithAnno", "default", map[string]string{AnnSecret: "mysecret"}, nil)
	testSecret1 := createSecret("mysecret", "default", "mysecretkey", "mysecretstring", map[string]string{AnnSecret: "mysecret"})
	testSecret2 := createSecret("mysecret2", "default", "mysecretkey2", "mysecretstring2", map[string]string{AnnSecret: "mysecret2"})

	// set test env
	myclient := k8sfake.NewSimpleClientset(pvcNoAnno, pvcWithAnno, testSecret1, testSecret2)

	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			name:    "expected to find secret",
			args:    args{myclient, pvcWithAnno},
			want:    "mysecret",
			wantErr: false,
		},
		{
			name:    "expected to not find secret",
			args:    args{myclient, pvcNoAnno},
			want:    "",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getSecretName(tt.args.client, tt.args.pvc)
			if (err != nil) != tt.wantErr {
				t.Errorf("getSecretName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("getSecretName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_updatePVC(t *testing.T) {
	type args struct {
		client kubernetes.Interface
		pvc    *v1.PersistentVolumeClaim
		anno   map[string]string
		label  map[string]string
	}
	//Create base pvc
	pvcNoAnno := createPvc("testPVC1", "default", nil, nil)

	tests := []struct {
		name    string
		args    args
		want    *v1.PersistentVolumeClaim
		wantErr bool
	}{
		{
			name:    "pvc is updated with annotation and label",
			args:    args{k8sfake.NewSimpleClientset(pvcNoAnno), pvcNoAnno, map[string]string{AnnCreatedBy: "cdi"}, map[string]string{CDILabelKey: CDILabelValue}},
			want:    createPvc("testPVC1", "default", map[string]string{AnnCreatedBy: "cdi"}, map[string]string{CDILabelKey: CDILabelValue}),
			wantErr: false,
		},
		{
			name:    "pvc is updated with annotation",
			args:    args{k8sfake.NewSimpleClientset(pvcNoAnno), pvcNoAnno, map[string]string{AnnCreatedBy: "cdi"}, nil},
			want:    createPvc("testPVC1", "default", map[string]string{AnnCreatedBy: "cdi"}, nil),
			wantErr: false,
		},
		{
			name:    "pvc is updated with label",
			args:    args{k8sfake.NewSimpleClientset(pvcNoAnno), pvcNoAnno, nil, map[string]string{CDILabelKey: CDILabelValue}},
			want:    createPvc("testPVC1", "default", nil, map[string]string{CDILabelKey: CDILabelValue}),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := updatePVC(tt.args.client, tt.args.pvc, tt.args.anno, tt.args.label)
			if (err != nil) != tt.wantErr {
				t.Errorf("updatePVC() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("updatePVC() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_setPVCAnnotation(t *testing.T) {
	type args struct {
		client kubernetes.Interface
		pvc    *v1.PersistentVolumeClaim
		key    string
		val    string
	}

	//Create base pvc
	pvcNoAnno := createPvc("testPVC1", "default", nil, nil)

	tests := []struct {
		name    string
		args    args
		want    *v1.PersistentVolumeClaim
		wantErr bool
	}{
		{
			name:    "pvc is updated with new annotation",
			args:    args{k8sfake.NewSimpleClientset(pvcNoAnno), pvcNoAnno, AnnCreatedBy, "cdi"},
			want:    createPvc("testPVC1", "default", map[string]string{AnnCreatedBy: "cdi"}, nil),
			wantErr: false,
		},
		{
			name:    "pvc is updated with new annotation - empty value",
			args:    args{k8sfake.NewSimpleClientset(pvcNoAnno), pvcNoAnno, AnnCreatedBy, ""},
			want:    createPvc("testPVC1", "default", map[string]string{AnnCreatedBy: ""}, nil),
			wantErr: false,
		},
		// TODO: Do we want to allow an Empty Key??
		{
			name:    "pvc is not updated with new annotation - empty key",
			args:    args{k8sfake.NewSimpleClientset(pvcNoAnno), pvcNoAnno, "", "cdi"},
			want:    createPvc("testPVC1", "default", map[string]string{"": "cdi"}, nil),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := setPVCAnnotation(tt.args.client, tt.args.pvc, tt.args.key, tt.args.val)
			if (err != nil) != tt.wantErr {
				t.Errorf("setPVCAnnotation() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("setPVCAnnotation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_checkIfAnnoExists(t *testing.T) {
	type args struct {
		pvc *v1.PersistentVolumeClaim
		key string
		val string
	}

	//create PVCs
	pvc := createPvc("testPVC", "default", map[string]string{AnnPodPhase: "Running"}, nil)
	pvcNoAnno := createPvc("testPVC2", "default", nil, nil)
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "pvc does have expected annotation key and value",
			args: args{pvc, AnnPodPhase, "Running"},
			want: true,
		},
		{
			name: "pvc does not have expected annotation key and value",
			args: args{pvc, AnnEndpoint, "http://test"},
			want: false,
		},
		{
			name: "pvc does have expected key but not value",
			args: args{pvc, AnnPodPhase, "Pending"},
			want: false,
		},
		{
			name: "pvc does not have any annotations",
			args: args{pvcNoAnno, AnnPodPhase, "Running"},
			want: false,
		},
		{
			name: "pvc does have expected annotations but pass in empty strings",
			args: args{pvc, "", ""},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkIfAnnoExists(tt.args.pvc, tt.args.key, tt.args.val); got != tt.want {
				t.Errorf("checkIfAnnoExists() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_checkIfLabelExists(t *testing.T) {
	type args struct {
		pvc *v1.PersistentVolumeClaim
		lbl string
		val string
	}
	//create PVCs
	pvc := createPvc("testPVC", "default", nil, map[string]string{CDILabelKey: CDILabelValue})
	pvcNoLbl := createPvc("testPVC2", "default", nil, nil)

	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "pvc does have expected label and expected value",
			args: args{pvc, CDILabelKey, CDILabelValue},
			want: true,
		},
		{
			name: "pvc does not have expected label",
			args: args{pvc, AnnCreatedBy, "yes"},
			want: false,
		},
		{
			name: "pvc does have expected label but does not have expected value",
			args: args{pvc, CDILabelKey, "something"},
			want: false,
		},
		{
			name: "pvc does not have any labels",
			args: args{pvcNoLbl, CDILabelKey, CDILabelValue},
			want: false,
		},
		{
			name: "pvc does have labels but pass in empty search strings",
			args: args{pvc, "", ""},
			want: false,
		},
	}
	// checkIfLabelExists expects both label to be present and correct value to match
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := checkIfLabelExists(tt.args.pvc, tt.args.lbl, tt.args.val); got != tt.want {
				t.Errorf("checkIfLabelExists() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMakeCDIConfigSpec(t *testing.T) {
	type args struct {
		name string
	}
	config := createCDIConfigWithStorageClass("testConfig", "")

	tests := []struct {
		name          string
		args          args
		wantCDIConfig *cdiv1.CDIConfig
	}{
		{
			name:          "expect CDIConfig to be created",
			args:          args{"testConfig"},
			wantCDIConfig: config,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MakeEmptyCDIConfigSpec(tt.args.name)

			if !reflect.DeepEqual(got, tt.wantCDIConfig) {
				t.Errorf("MakeEmptyCDIConfigSpec() =\n%v\n, want\n%v", got, tt.wantCDIConfig)
			}

		})
	}
}

func Test_addToMap(t *testing.T) {
	type args struct {
		m1 map[string]string
		m2 map[string]string
	}
	tests := []struct {
		name string
		args args
		want map[string]string
	}{
		{
			name: "use different key for map1 and map2 expect both maps to be returned",
			args: args{map[string]string{AnnImportPod: "mypod"}, map[string]string{CDILabelKey: CDILabelValue}},
			want: map[string]string{AnnImportPod: "mypod", CDILabelKey: CDILabelValue},
		},
		{
			name: "use same key for map1 and map2 expect map2 to be returned",
			args: args{map[string]string{AnnImportPod: "mypod"}, map[string]string{AnnImportPod: "map2pod"}},
			want: map[string]string{AnnImportPod: "map2pod"},
		},
		{
			name: "pass in empty map1 and map2 expect empty map",
			args: args{nil, nil},
			want: map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addToMap(tt.args.m1, tt.args.m2); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("addToMap() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getCertConfigMap(t *testing.T) {
	namespace := "default"
	configMapName := "foobar"

	testPVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"cdi.kubevirt.io/storage.import.certConfigMap": configMapName,
			},
		},
	}

	testConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
	}

	type args struct {
		pvc    *corev1.PersistentVolumeClaim
		objs   []runtime.Object
		result string
	}

	for _, arg := range []args{
		{&corev1.PersistentVolumeClaim{}, nil, ""},
		{testPVC, []runtime.Object{testConfigMap}, configMapName},
		{testPVC, nil, configMapName},
	} {
		client := k8sfake.NewSimpleClientset(arg.objs...)

		result, err := getCertConfigMap(client, arg.pvc)

		if err != nil {
			t.Errorf("Enexpected error %+v", err)
		}

		if result != arg.result {
			t.Errorf("Expected %s got %s", arg.result, result)
		}
	}
}

func Test_getInsecureTLS(t *testing.T) {
	namespace := "cdi"
	configMapName := "cdi-insecure-registries"
	host := "myregistry"
	endpointNoPort := "docker://" + host
	hostWithPort := host + ":5000"
	endpointWithPort := "docker://" + hostWithPort

	type args struct {
		endpoint       string
		confiMapExists bool
		insecureHost   string
		result         bool
	}

	for _, arg := range []args{
		{endpointNoPort, true, host, true},
		{endpointWithPort, true, hostWithPort, true},
		{endpointNoPort, true, hostWithPort, false},
		{endpointWithPort, true, host, false},
		{endpointNoPort, false, "", false},
		{"", true, host, false},
	} {
		var objs []runtime.Object

		pvc := &corev1.PersistentVolumeClaim{}
		if arg.endpoint != "" {
			pvc.Annotations = map[string]string{
				"cdi.kubevirt.io/storage.import.endpoint": arg.endpoint,
			}
		}

		if arg.confiMapExists {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: namespace,
				},
			}

			if arg.insecureHost != "" {
				cm.Data = map[string]string{
					"test-registry": arg.insecureHost,
				}
			}

			objs = append(objs, cm)
		}

		client := k8sfake.NewSimpleClientset(objs...)

		result, err := isInsecureTLS(client, pvc)

		if err != nil {
			t.Errorf("Enexpected error %+v", err)
		}

		if result != arg.result {
			t.Errorf("Expected %t got %t", arg.result, result)
		}
	}
}

func Test_GetScratchPvcStorageClassDefault(t *testing.T) {
	var objs []runtime.Object
	objs = append(objs, createStorageClass("test1", nil))
	objs = append(objs, createStorageClass("test2", nil))
	objs = append(objs, createStorageClass("test3", map[string]string{
		AnnDefaultStorageClass: "true",
	}))
	client := k8sfake.NewSimpleClientset(objs...)

	storageClassName := "test3"
	var cdiObjs []runtime.Object
	cdiObjs = append(cdiObjs, createCDIConfigWithStorageClass(common.ConfigName, storageClassName))
	cdiclient := cdifake.NewSimpleClientset(cdiObjs...)

	pvc := createPvc("test", "test", nil, nil)
	result := GetScratchPvcStorageClass(client, cdiclient, pvc)

	if result != storageClassName {
		t.Error("Storage class is not test3")
	}
}

func Test_GetScratchPvcStorageClassConfig(t *testing.T) {
	var objs []runtime.Object
	objs = append(objs, createStorageClass("test1", nil))
	objs = append(objs, createStorageClass("test2", nil))
	objs = append(objs, createStorageClass("test3", map[string]string{
		AnnDefaultStorageClass: "true",
	}))
	client := k8sfake.NewSimpleClientset(objs...)

	storageClassName := "test1"
	var cdiObjs []runtime.Object
	config := createCDIConfigWithStorageClass(common.ConfigName, storageClassName)
	config.Spec.ScratchSpaceStorageClass = &storageClassName
	cdiObjs = append(cdiObjs, config)
	cdiclient := cdifake.NewSimpleClientset(cdiObjs...)

	pvc := createPvc("test", "test", nil, nil)
	result := GetScratchPvcStorageClass(client, cdiclient, pvc)

	if result != storageClassName {
		t.Error("Storage class is not test1")
	}
}

func Test_GetScratchPvcStorageClassPvc(t *testing.T) {
	var objs []runtime.Object
	client := k8sfake.NewSimpleClientset(objs...)

	storageClass := "storageClass"
	var cdiObjs []runtime.Object
	cdiObjs = append(cdiObjs, createCDIConfigWithStorageClass(common.ConfigName, storageClass))
	cdiclient := cdifake.NewSimpleClientset(cdiObjs...)

	pvc := createPvcInStorageClass("test", "test", &storageClass, nil, nil)
	result := GetScratchPvcStorageClass(client, cdiclient, pvc)

	if result != storageClass {
		t.Error("Storage class is not storageClass")
	}
}

func Test_DecodePublicKey(t *testing.T) {
	bytes, err := cert.EncodePublicKeyPEM(&getAPIServerKey().PublicKey)
	if err != nil {
		t.Errorf("error encoding public key")
	}

	_, err = DecodePublicKey(bytes)
	if err != nil {
		t.Errorf("error decoding public key")
	}
}

func Test_TokenValidation(t *testing.T) {

	goodTokenData := func() *token.Payload {
		return &token.Payload{
			Operation: token.OperationClone,
			Name:      "source",
			Namespace: "sourcens",
			Resource: metav1.GroupVersionResource{
				Resource: "persistentvolumeclaims",
			},
			Params: map[string]string{
				"targetName":      "target",
				"targetNamespace": "targetns",
			},
		}
	}

	badOperation := goodTokenData()
	badOperation.Operation = token.OperationUpload

	badSourceName := goodTokenData()
	badSourceName.Name = "foo"

	badSourceNamespace := goodTokenData()
	badSourceNamespace.Namespace = "foo"

	badResource := goodTokenData()
	badResource.Resource.Resource = "foo"

	badTargetName := goodTokenData()
	badTargetName.Params["targetName"] = "foo"

	badTargetNamespace := goodTokenData()
	badTargetNamespace.Params["targetNamespace"] = "foo"

	missingParams := goodTokenData()
	missingParams.Params = nil

	g := token.NewGenerator(common.CloneTokenIssuer, getAPIServerKey(), 5*time.Minute)
	v := newCloneTokenValidator(&getAPIServerKey().PublicKey)

	payloads := []*token.Payload{
		goodTokenData(),
		badOperation,
		badSourceName,
		badSourceNamespace,
		badResource,
		badTargetName,
		badTargetNamespace,
		missingParams,
	}

	for _, p := range payloads {
		tokenString, err := g.Generate(p)
		if err != nil {
			panic("error generating token")
		}

		source := &v1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "source",
				Namespace: "sourcens",
			},
		}

		target := &v1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "target",
				Namespace: "targetns",
				Annotations: map[string]string{
					AnnCloneToken: tokenString,
				},
			},
		}

		err = validateCloneToken(v, source, target)
		if err == nil && !reflect.DeepEqual(p, goodTokenData()) {
			t.Error("validation should have failed")
		} else if err != nil && reflect.DeepEqual(p, goodTokenData()) {
			t.Error("validation should have succeeded")
		}
	}
}

func createBlockPvc(name, ns string, annotations, labels map[string]string) *v1.PersistentVolumeClaim {
	pvcDef := createPvcInStorageClass(name, ns, nil, annotations, labels)
	volumeMode := v1.PersistentVolumeBlock
	pvcDef.Spec.VolumeMode = &volumeMode
	return pvcDef
}

func createPvc(name, ns string, annotations, labels map[string]string) *v1.PersistentVolumeClaim {
	return createPvcInStorageClass(name, ns, nil, annotations, labels)
}

func createPvcInStorageClass(name, ns string, storageClassName *string, annotations, labels map[string]string) *v1.PersistentVolumeClaim {
	return &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: annotations,
			Labels:      labels,
			UID:         types.UID(ns + "-" + name),
		},
		Spec: v1.PersistentVolumeClaimSpec{
			AccessModes: []v1.PersistentVolumeAccessMode{v1.ReadOnlyMany, v1.ReadWriteOnce},
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceName(v1.ResourceStorage): resource.MustParse("1G"),
				},
			},
			StorageClassName: storageClassName,
		},
	}
}

func createScratchPvc(pvc *v1.PersistentVolumeClaim, pod *v1.Pod, storageClassName string) *v1.PersistentVolumeClaim {
	t := true
	labels := map[string]string{
		"cdi-controller": pod.Name,
		"app":            "containerized-data-importer",
		LabelImportPvc:   pvc.Name,
	}
	annotations := make(map[string]string)
	if len(pvc.GetAnnotations()) > 0 {
		for k, v := range pvc.GetAnnotations() {
			if strings.Contains(k, common.KubeVirtAnnKey) && !strings.Contains(k, common.CDIAnnKey) {
				annotations[k] = v
			}
		}
	}

	return &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        pvc.Name + "-scratch",
			Namespace:   pvc.Namespace,
			Labels:      labels,
			Annotations: annotations,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "v1",
					Kind:               "Pod",
					Name:               pod.Name,
					UID:                pod.GetUID(),
					Controller:         &t,
					BlockOwnerDeletion: &t,
				},
			},
		},
		Spec: v1.PersistentVolumeClaimSpec{
			AccessModes:      []v1.PersistentVolumeAccessMode{"ReadWriteOnce"},
			Resources:        pvc.Spec.Resources,
			StorageClassName: &storageClassName,
		},
	}
}

func createPvcNoSize(name, ns string, annotations, labels map[string]string) *v1.PersistentVolumeClaim {
	return &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: annotations,
			Labels:      labels,
		},
	}
}

func createSecret(name, ns, accessKey, secretKey string, labels map[string]string) *v1.Secret {
	return &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Type: "Opaque",
		Data: map[string][]byte{
			bootstrapapi.BootstrapTokenIDKey:           []byte(accessKey),
			bootstrapapi.BootstrapTokenSecretKey:       []byte(secretKey),
			bootstrapapi.BootstrapTokenUsageSigningKey: []byte("true"),
		},
	}
}

func getPvcKey(pvc *corev1.PersistentVolumeClaim, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pvc)
	if err != nil {
		t.Errorf("Unexpected error getting key for pvc %v: %v", pvc.Name, err)
		return ""
	}
	return key
}

func createCDIConfig(name string) *cdiv1.CDIConfig {
	return createCDIConfigWithStorageClass(name, "")
}

func createCDIConfigWithStorageClass(name string, storageClass string) *cdiv1.CDIConfig {
	return &cdiv1.CDIConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				common.CDILabelKey:       common.CDILabelValue,
				common.CDIComponentLabel: "",
			},
		},
		Status: cdiv1.CDIConfigStatus{
			ScratchSpaceStorageClass: storageClass,
		},
	}
}

func createStorageClass(name string, annotations map[string]string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
	}
}

func createStorageClassWithProvisioner(name string, annotations map[string]string, provisioner string) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		Provisioner: provisioner,
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
	}
}
func createSnapshotClass(name string, annotations map[string]string, snapshotter string) *snapshotv1.VolumeSnapshotClass {
	return &snapshotv1.VolumeSnapshotClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VolumeSnapshotClass",
			APIVersion: snapshotv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
		},
		Snapshotter: snapshotter,
	}
}

func createVolumeSnapshotContentCrd() *apiextensionsv1beta1.CustomResourceDefinition {
	return &apiextensionsv1beta1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CustomResourceDefinition",
			APIVersion: apiextensionsv1beta1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: crdv1.VolumeSnapshotContentResourcePlural + "." + crdv1.GroupName,
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   crdv1.GroupName,
			Version: crdv1.SchemeGroupVersion.Version,
			Scope:   apiextensionsv1beta1.ClusterScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural: crdv1.VolumeSnapshotContentResourcePlural,
				Kind:   reflect.TypeOf(crdv1.VolumeSnapshotContent{}).Name(),
			},
		},
	}
}

func createVolumeSnapshotClassCrd() *apiextensionsv1beta1.CustomResourceDefinition {
	return &apiextensionsv1beta1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CustomResourceDefinition",
			APIVersion: apiextensionsv1beta1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: crdv1.VolumeSnapshotClassResourcePlural + "." + crdv1.GroupName,
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   crdv1.GroupName,
			Version: crdv1.SchemeGroupVersion.Version,
			Scope:   apiextensionsv1beta1.ClusterScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural: crdv1.VolumeSnapshotClassResourcePlural,
				Kind:   reflect.TypeOf(crdv1.VolumeSnapshotClass{}).Name(),
			},
		},
	}
}

func createVolumeSnapshotCrd() *apiextensionsv1beta1.CustomResourceDefinition {
	return &apiextensionsv1beta1.CustomResourceDefinition{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CustomResourceDefinition",
			APIVersion: apiextensionsv1beta1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: crdv1.VolumeSnapshotResourcePlural + "." + crdv1.GroupName,
		},
		Spec: apiextensionsv1beta1.CustomResourceDefinitionSpec{
			Group:   crdv1.GroupName,
			Version: crdv1.SchemeGroupVersion.Version,
			Scope:   apiextensionsv1beta1.NamespaceScoped,
			Names: apiextensionsv1beta1.CustomResourceDefinitionNames{
				Plural: crdv1.VolumeSnapshotResourcePlural,
				Kind:   reflect.TypeOf(crdv1.VolumeSnapshot{}).Name(),
			},
		},
	}
}

func createDefaultPodResourceRequirements(limitCPUValue int64, limitMemoryValue int64, requestCPUValue int64, requestMemoryValue int64) *corev1.ResourceRequirements {
	return &corev1.ResourceRequirements{
		Limits: map[corev1.ResourceName]resource.Quantity{
			corev1.ResourceCPU:    *resource.NewQuantity(limitCPUValue, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(limitMemoryValue, resource.DecimalSI)},
		Requests: map[corev1.ResourceName]resource.Quantity{
			corev1.ResourceCPU:    *resource.NewQuantity(requestCPUValue, resource.DecimalSI),
			corev1.ResourceMemory: *resource.NewQuantity(requestMemoryValue, resource.DecimalSI)},
	}
}
