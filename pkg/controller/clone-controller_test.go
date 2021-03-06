package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"sync"
	"time"

	"kubevirt.io/containerized-data-importer/pkg/util/cert/fetcher"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"

	cdiv1 "kubevirt.io/containerized-data-importer/pkg/apis/core/v1alpha1"
	"kubevirt.io/containerized-data-importer/pkg/common"
	"kubevirt.io/containerized-data-importer/pkg/token"
	"kubevirt.io/containerized-data-importer/pkg/util/cert/triple"
)

var (
	apiServerKey     *rsa.PrivateKey
	apiServerKeyOnce sync.Once
)

type fakeCertGenerator struct {
}

func (cg *fakeCertGenerator) MakeClientCert(name string, groups []string, duration time.Duration) ([]byte, []byte, error) {
	return []byte("foo"), []byte("bar"), nil
}

func (cg *fakeCertGenerator) MakeServerCert(namespace, service string, duration time.Duration) ([]byte, []byte, error) {
	return []byte("foo"), []byte("bar"), nil
}

type FakeValidator struct {
	match     string
	Operation token.Operation
	Name      string
	Namespace string
	Resource  metav1.GroupVersionResource
	Params    map[string]string
}

var _ = Describe("Clone controller reconcile loop", func() {
	var (
		reconciler *CloneReconciler
	)
	AfterEach(func() {
		if reconciler != nil {
			close(reconciler.recorder.(*record.FakeRecorder).Events)
			reconciler = nil
		}
	})

	It("Should return success if a PVC with no annotations is passed, due to it being ignored", func() {
		reconciler = createCloneReconciler(createPvc("testPvc1", "default", map[string]string{}, nil))
		_, err := reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should return success if no PVC can be found, due to it not existing", func() {
		reconciler = createCloneReconciler()
		_, err := reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should return success if no PVC can be found due to not existing in passed namespace", func() {
		reconciler = createCloneReconciler(createPvc("testPvc1", "default", map[string]string{AnnEndpoint: testEndPoint}, nil))
		_, err := reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "invalid"}})
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should return success if a PVC with clone request annotation and cloneof is passed, due to it being ignored", func() {
		reconciler = createCloneReconciler(createPvc("testPvc1", "default", map[string]string{AnnCloneRequest: "cloneme", AnnCloneOf: "something"}, nil))
		_, err := reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should return success if target pod is not ready", func() {
		reconciler = createCloneReconciler(createPvc("testPvc1", "default", map[string]string{AnnCloneRequest: "cloneme"}, nil))
		_, err := reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should return error if target pod annotation is invalid", func() {
		reconciler = createCloneReconciler(createPvc("testPvc1", "default", map[string]string{AnnCloneRequest: "cloneme", AnnPodReady: "invalid"}, nil))
		_, err := reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("error parsing %s annotation", AnnPodReady)))
	})

	It("Should create new source pod if none exists, and target pod is marked ready", func() {
		testPvc := createPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz", AnnUploadClientName: "uploadclient"}, nil)
		reconciler = createCloneReconciler(testPvc, createPvc("source", "default", map[string]string{}, nil))
		By("Setting up the match token")
		reconciler.tokenValidator.(*FakeValidator).match = "foobaz"
		reconciler.tokenValidator.(*FakeValidator).Name = "source"
		reconciler.tokenValidator.(*FakeValidator).Namespace = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetNamespace"] = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetName"] = "testPvc1"
		By("Verifying no source pod exists")
		sourcePod, err := reconciler.findCloneSourcePod(testPvc)
		Expect(sourcePod).To(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
		By("Verifying source pod exists")
		sourcePod, err = reconciler.findCloneSourcePod(testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(sourcePod.GetLabels()[CloneUniqueID]).To(Equal("default-testPvc1-source-pod"))
		By("Verifying the PVC now has a finalizer")
		err = reconciler.Client.Get(context.TODO(), types.NamespacedName{Name: "testPvc1", Namespace: "default"}, testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(reconciler.hasFinalizer(testPvc, cloneSourcePodFinalizer)).To(BeTrue())
	})

	It("Should error with missing upload client name annotation if none provided", func() {
		testPvc := createPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz"}, nil)
		reconciler = createCloneReconciler(testPvc, createPvc("source", "default", map[string]string{}, nil))
		By("Setting up the match token")
		reconciler.tokenValidator.(*FakeValidator).match = "foobaz"
		reconciler.tokenValidator.(*FakeValidator).Name = "source"
		reconciler.tokenValidator.(*FakeValidator).Namespace = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetNamespace"] = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetName"] = "testPvc1"
		By("Verifying no source pod exists")
		sourcePod, err := reconciler.findCloneSourcePod(testPvc)
		Expect(sourcePod).To(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("missing required " + AnnUploadClientName + " annotation"))
	})

	It("Should update the PVC from the pod status", func() {
		testPvc := createPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz", AnnUploadClientName: "uploadclient"}, nil)
		reconciler = createCloneReconciler(testPvc, createPvc("source", "default", map[string]string{}, nil))
		By("Setting up the match token")
		reconciler.tokenValidator.(*FakeValidator).match = "foobaz"
		reconciler.tokenValidator.(*FakeValidator).Name = "source"
		reconciler.tokenValidator.(*FakeValidator).Namespace = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetNamespace"] = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetName"] = "testPvc1"
		By("Verifying no source pod exists")
		sourcePod, err := reconciler.findCloneSourcePod(testPvc)
		Expect(sourcePod).To(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
		By("Verifying source pod exists")
		sourcePod, err = reconciler.findCloneSourcePod(testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(sourcePod.GetLabels()[CloneUniqueID]).To(Equal("default-testPvc1-source-pod"))
		By("Verifying the PVC now has a finalizer")
		err = reconciler.Client.Get(context.TODO(), types.NamespacedName{Name: "testPvc1", Namespace: "default"}, testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(reconciler.hasFinalizer(testPvc, cloneSourcePodFinalizer)).To(BeTrue())
	})

	It("Should update the cloneof when complete", func() {
		testPvc := createPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz", AnnUploadClientName: "uploadclient"}, nil)
		reconciler = createCloneReconciler(testPvc, createPvc("source", "default", map[string]string{}, nil))
		By("Setting up the match token")
		reconciler.tokenValidator.(*FakeValidator).match = "foobaz"
		reconciler.tokenValidator.(*FakeValidator).Name = "source"
		reconciler.tokenValidator.(*FakeValidator).Namespace = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetNamespace"] = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetName"] = "testPvc1"
		By("Verifying no source pod exists")
		sourcePod, err := reconciler.findCloneSourcePod(testPvc)
		Expect(sourcePod).To(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
		By("Verifying source pod exists")
		sourcePod, err = reconciler.findCloneSourcePod(testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(sourcePod.GetLabels()[CloneUniqueID]).To(Equal("default-testPvc1-source-pod"))
		By("Verifying the PVC now has a finalizer")
		err = reconciler.Client.Get(context.TODO(), types.NamespacedName{Name: "testPvc1", Namespace: "default"}, testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(reconciler.hasFinalizer(testPvc, cloneSourcePodFinalizer)).To(BeTrue())
		By("Updating the PVC to completed")
		testPvc = createPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz", AnnUploadClientName: "uploadclient", AnnPodPhase: string(corev1.PodSucceeded)}, nil)
		reconciler.Client.Update(context.TODO(), testPvc)
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
		err = reconciler.Client.Get(context.TODO(), types.NamespacedName{Name: "testPvc1", Namespace: "default"}, testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(testPvc.GetAnnotations()[AnnCloneOf]).To(Equal("true"))
		sourcePod, err = reconciler.findCloneSourcePod(testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(sourcePod).ToNot(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
		By("Checking error event recorded")
		event := <-reconciler.recorder.(*record.FakeRecorder).Events
		Expect(event).To(ContainSubstring("Clone Successful"))
		sourcePod, err = reconciler.findCloneSourcePod(testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(sourcePod).To(BeNil())
	})

	It("Should update the cloneof when complete, block mode", func() {
		testPvc := createBlockPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz", AnnUploadClientName: "uploadclient"}, nil)
		reconciler = createCloneReconciler(testPvc, createBlockPvc("source", "default", map[string]string{}, nil))
		By("Setting up the match token")
		reconciler.tokenValidator.(*FakeValidator).match = "foobaz"
		reconciler.tokenValidator.(*FakeValidator).Name = "source"
		reconciler.tokenValidator.(*FakeValidator).Namespace = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetNamespace"] = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetName"] = "testPvc1"
		By("Verifying no source pod exists")
		sourcePod, err := reconciler.findCloneSourcePod(testPvc)
		Expect(sourcePod).To(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
		By("Verifying source pod exists")
		sourcePod, err = reconciler.findCloneSourcePod(testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(sourcePod.GetLabels()[CloneUniqueID]).To(Equal("default-testPvc1-source-pod"))
		By("Verifying the PVC now has a finalizer")
		err = reconciler.Client.Get(context.TODO(), types.NamespacedName{Name: "testPvc1", Namespace: "default"}, testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(reconciler.hasFinalizer(testPvc, cloneSourcePodFinalizer)).To(BeTrue())
		By("Updating the PVC to completed")
		testPvc = createPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz", AnnUploadClientName: "uploadclient", AnnPodPhase: string(corev1.PodSucceeded)}, nil)
		reconciler.Client.Update(context.TODO(), testPvc)
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
		err = reconciler.Client.Get(context.TODO(), types.NamespacedName{Name: "testPvc1", Namespace: "default"}, testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(testPvc.GetAnnotations()[AnnCloneOf]).To(Equal("true"))
		sourcePod, err = reconciler.findCloneSourcePod(testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(sourcePod).ToNot(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).ToNot(HaveOccurred())
		By("Checking error event recorded")
		event := <-reconciler.recorder.(*record.FakeRecorder).Events
		Expect(event).To(ContainSubstring("Clone Successful"))
		sourcePod, err = reconciler.findCloneSourcePod(testPvc)
		Expect(err).ToNot(HaveOccurred())
		Expect(sourcePod).To(BeNil())
	})

	It("Should error when source and target volume modes do not match (fs->block)", func() {
		testPvc := createBlockPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz", AnnUploadClientName: "uploadclient"}, nil)
		reconciler = createCloneReconciler(testPvc, createPvc("source", "default", map[string]string{}, nil))
		By("Setting up the match token")
		reconciler.tokenValidator.(*FakeValidator).match = "foobaz"
		reconciler.tokenValidator.(*FakeValidator).Name = "source"
		reconciler.tokenValidator.(*FakeValidator).Namespace = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetNamespace"] = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetName"] = "testPvc1"
		By("Verifying no source pod exists")
		sourcePod, err := reconciler.findCloneSourcePod(testPvc)
		Expect(sourcePod).To(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("source volumeMode (Filesystem) and target volumeMode (Block) do not match"))
	})

	It("Should error when source and target volume modes do not match (fs->block)", func() {
		testPvc := createPvc("testPvc1", "default", map[string]string{
			AnnCloneRequest: "default/source", AnnPodReady: "true", AnnCloneToken: "foobaz", AnnUploadClientName: "uploadclient"}, nil)
		reconciler = createCloneReconciler(testPvc, createBlockPvc("source", "default", map[string]string{}, nil))
		By("Setting up the match token")
		reconciler.tokenValidator.(*FakeValidator).match = "foobaz"
		reconciler.tokenValidator.(*FakeValidator).Name = "source"
		reconciler.tokenValidator.(*FakeValidator).Namespace = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetNamespace"] = "default"
		reconciler.tokenValidator.(*FakeValidator).Params["targetName"] = "testPvc1"
		By("Verifying no source pod exists")
		sourcePod, err := reconciler.findCloneSourcePod(testPvc)
		Expect(sourcePod).To(BeNil())
		_, err = reconciler.Reconcile(reconcile.Request{NamespacedName: types.NamespacedName{Name: "testPvc1", Namespace: "default"}})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("source volumeMode (Block) and target volumeMode (Filesystem) do not match"))
	})
})

var _ = Describe("ParseCloneRequestAnnotation", func() {
	It("should return false/empty/empty if no annotation exists", func() {
		pvc := createPvc("testPvc1", "default", map[string]string{}, nil)
		exists, ns, name := ParseCloneRequestAnnotation(pvc)
		Expect(exists).To(BeFalse())
		Expect(ns).To(BeEmpty())
		Expect(name).To(BeEmpty())
	})

	It("should return false/empty/empty if annotation is invalid", func() {
		pvc := createPvc("testPvc1", "default", map[string]string{AnnCloneRequest: "default"}, nil)
		exists, ns, name := ParseCloneRequestAnnotation(pvc)
		Expect(exists).To(BeFalse())
		Expect(ns).To(BeEmpty())
		Expect(name).To(BeEmpty())
		pvc = createPvc("testPvc1", "default", map[string]string{AnnCloneRequest: "default/test/something"}, nil)
		exists, ns, name = ParseCloneRequestAnnotation(pvc)
		Expect(exists).To(BeFalse())
		Expect(ns).To(BeEmpty())
		Expect(name).To(BeEmpty())
	})

	It("should return true/default/test if annotation is valid", func() {
		pvc := createPvc("testPvc1", "default", map[string]string{AnnCloneRequest: "default/test"}, nil)
		exists, ns, name := ParseCloneRequestAnnotation(pvc)
		Expect(exists).To(BeTrue())
		Expect(ns).To(Equal("default"))
		Expect(name).To(Equal("test"))
	})
})

var _ = Describe("CloneSourcePodName", func() {
	It("Should be unique and deterministic", func() {
		pvc1d := createPvc("testPvc1", "default", map[string]string{AnnCloneRequest: "default/test"}, nil)
		pvc1d2 := createPvc("testPvc1", "default2", map[string]string{AnnCloneRequest: "default/test"}, nil)
		pvc2d1 := createPvc("testPvc2", "default", map[string]string{AnnCloneRequest: "default/test"}, nil)
		pvcSimilar := createPvc("testP", "vc1default", map[string]string{AnnCloneRequest: "default/test"}, nil)
		podName1d := getCloneSourcePodName(pvc1d)
		podName1dagain := getCloneSourcePodName(pvc1d)
		By("Verifying rerunning getloneSourcePodName on same PVC I get same name")
		Expect(podName1d).To(Equal(podName1dagain))
		By("Verifying different namespace but same name I get different pod name")
		podName1d2 := getCloneSourcePodName(pvc1d2)
		Expect(podName1d).NotTo(Equal(podName1d2))
		By("Verifying same namespace but different name I get different pod name")
		podName2d1 := getCloneSourcePodName(pvc2d1)
		Expect(podName1d).NotTo(Equal(podName2d1))
		By("Verifying concatenated ns/name of same characters I get different pod name")
		podNameSimilar := getCloneSourcePodName(pvcSimilar)
		Expect(podName1d).NotTo(Equal(podNameSimilar))
	})
})

func createCloneReconciler(objects ...runtime.Object) *CloneReconciler {
	objs := []runtime.Object{}
	objs = append(objs, objects...)
	cdiConfig := MakeEmptyCDIConfigSpec(common.ConfigName)
	cdiConfig.Status = cdiv1.CDIConfigStatus{
		DefaultPodResourceRequirements: createDefaultPodResourceRequirements(int64(0), int64(0), int64(0), int64(0)),
	}
	objs = append(objs, cdiConfig)

	// Register operator types with the runtime scheme.
	s := scheme.Scheme
	cdiv1.AddToScheme(s)
	rec := record.NewFakeRecorder(1)
	// Create a fake client to mock API calls.
	cl := fake.NewFakeClientWithScheme(s, objs...)
	k8sfakeclientset := k8sfake.NewSimpleClientset(createStorageClass(testStorageClass, nil))

	// Create a ReconcileMemcached object with the scheme and fake client.
	return &CloneReconciler{
		Client:   cl,
		Scheme:   s,
		Log:      log,
		recorder: rec,
		tokenValidator: &FakeValidator{
			Params: make(map[string]string, 0),
		},
		K8sClient:           k8sfakeclientset,
		Image:               testImage,
		clientCertGenerator: &fakeCertGenerator{},
		serverCAFetcher:     &fetcher.MemCertBundleFetcher{Bundle: []byte("baz")},
	}
}

func testCreateClientKeyAndCert(ca *triple.KeyPair, commonName string, organizations []string) ([]byte, []byte, error) {
	return []byte("foo"), []byte("bar"), nil
}

func getAPIServerKey() *rsa.PrivateKey {
	apiServerKeyOnce.Do(func() {
		apiServerKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	})
	return apiServerKey
}

func (v *FakeValidator) Validate(value string) (*token.Payload, error) {
	if value != v.match {
		return nil, fmt.Errorf("Token does not match expected")
	}
	resource := metav1.GroupVersionResource{
		Resource: "persistentvolumeclaims",
	}
	return &token.Payload{
		Name:      v.Name,
		Namespace: v.Namespace,
		Operation: token.OperationClone,
		Resource:  resource,
		Params:    v.Params,
	}, nil
}

func createClonePvc(sourceNamespace, sourceName, targetNamespace, targetName string, annotations, labels map[string]string) *corev1.PersistentVolumeClaim {
	return createClonePvcWithSize(sourceNamespace, sourceName, targetNamespace, targetName, annotations, labels, "1G")
}

func createClonePvcWithSize(sourceNamespace, sourceName, targetNamespace, targetName string, annotations, labels map[string]string, size string) *corev1.PersistentVolumeClaim {
	tokenData := &token.Payload{
		Operation: token.OperationClone,
		Name:      sourceName,
		Namespace: sourceNamespace,
		Resource: metav1.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "persistentvolumeclaims",
		},
		Params: map[string]string{
			"targetNamespace": targetNamespace,
			"targetName":      targetName,
		},
	}

	g := token.NewGenerator(common.CloneTokenIssuer, getAPIServerKey(), 5*time.Minute)

	tokenString, err := g.Generate(tokenData)
	if err != nil {
		panic("error generating token")
	}

	if annotations == nil {
		annotations = make(map[string]string)
	}

	annotations[AnnCloneRequest] = fmt.Sprintf("%s/%s", sourceNamespace, sourceName)
	annotations[AnnCloneToken] = tokenString
	annotations[AnnUploadClientName] = "FOOBAR"

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        targetName,
			Namespace:   targetNamespace,
			Annotations: annotations,
			Labels:      labels,
			UID:         "pvc-uid",
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadOnlyMany, corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceName(corev1.ResourceStorage): resource.MustParse(size),
				},
			},
		},
	}
}

func createCloneBlockPvc(sourceNamespace, sourceName, targetNamespace, targetName string, annotations, labels map[string]string) *corev1.PersistentVolumeClaim {
	pvc := createClonePvc(sourceNamespace, sourceName, targetNamespace, targetName, annotations, labels)
	VolumeMode := corev1.PersistentVolumeBlock
	pvc.Spec.VolumeMode = &VolumeMode
	return pvc
}

func createSourcePod(pvc *corev1.PersistentVolumeClaim, pvcUID string) *corev1.Pod {
	_, _, sourcePvcName := ParseCloneRequestAnnotation(pvc)
	podName := fmt.Sprintf("%s-%s-", common.ClonerSourcePodName, sourcePvcName)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: podName,
			Annotations: map[string]string{
				AnnCreatedBy: "yes",
				AnnOwnerRef:  fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name),
			},
			Labels: map[string]string{
				common.CDILabelKey:       common.CDILabelValue, //filtered by the podInformer
				common.CDIComponentLabel: common.ClonerSourcePodName,
				// this label is used when searching for a pvc's cloner source pod.
				CloneUniqueID:          pvcUID + "-source-pod",
				common.PrometheusLabel: "",
			},
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser: &[]int64{0}[0],
			},
			Containers: []corev1.Container{
				{
					Name:            common.ClonerSourcePodName,
					Image:           "test/mycloneimage",
					ImagePullPolicy: corev1.PullAlways,
					Env: []corev1.EnvVar{
						{
							Name:  "CLIENT_KEY",
							Value: "bar",
						},
						{
							Name:  "CLIENT_CERT",
							Value: "foo",
						},
						{
							Name:  "SERVER_CA_CERT",
							Value: string("baz"),
						},
						{
							Name:  "UPLOAD_URL",
							Value: GetUploadServerURL(pvc.Namespace, pvc.Name, common.UploadPathSync),
						},
						{
							Name:  common.OwnerUID,
							Value: "",
						},
					},
					Ports: []corev1.ContainerPort{
						{
							Name:          "metrics",
							ContainerPort: 8443,
							Protocol:      corev1.ProtocolTCP,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyOnFailure,
			Volumes: []corev1.Volume{
				{
					Name: DataVolName,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: sourcePvcName,
							ReadOnly:  false,
						},
					},
				},
			},
		},
	}

	var volumeMode corev1.PersistentVolumeMode
	var addVars []corev1.EnvVar

	if pvc.Spec.VolumeMode != nil {
		volumeMode = *pvc.Spec.VolumeMode
	} else {
		volumeMode = corev1.PersistentVolumeFilesystem
	}

	if volumeMode == corev1.PersistentVolumeBlock {
		pod.Spec.Containers[0].VolumeDevices = addVolumeDevices()
		addVars = []corev1.EnvVar{
			{
				Name:  "VOLUME_MODE",
				Value: "block",
			},
			{
				Name:  "MOUNT_POINT",
				Value: common.WriteBlockPath,
			},
		}
		pod.Spec.SecurityContext = &corev1.PodSecurityContext{
			RunAsUser: &[]int64{0}[0],
		}
	} else {
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{
				Name:      DataVolName,
				MountPath: common.ClonerMountPath,
			},
		}
		addVars = []corev1.EnvVar{
			{
				Name:  "VOLUME_MODE",
				Value: "filesystem",
			},
			{
				Name:  "MOUNT_POINT",
				Value: common.ClonerMountPath,
			},
		}
	}

	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, addVars...)

	return pod
}
