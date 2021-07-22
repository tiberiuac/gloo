package printers

import (
	"os"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	v1 "github.com/solo-io/gloo/projects/gloo/pkg/api/v1"
	"github.com/solo-io/solo-kit/pkg/api/v1/resources/core"
)

var _ = Describe("UpstreamTable", func() {

	BeforeEach(func() {
		Expect(os.Setenv("POD_NAMESPACE", "gloo-system")).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		Expect(os.Unsetenv("POD_NAMESPACE")).NotTo(HaveOccurred())
	})

	It("handles malformed upstream (nil spec)", func() {
		Expect(func() {
			us := &v1.Upstream{}
			UpstreamTable(nil, []*v1.Upstream{us}, GinkgoWriter)
		}).NotTo(Panic())
	})

	It("handles statuses from multiple controllers", func() {
		us := &v1.Upstream{}
		us.AddToReporterStatus(&core.Status{
			State:      core.Status_Accepted,
			ReportedBy: "gloo",
		})
		us.AddToReporterStatus(&core.Status{
			State:      core.Status_Pending,
			ReportedBy: "gateway",
		})
		Expect(upstreamStatus(us)).To(ContainSubstring("gloo-system:gloo: Accepted"))
		Expect(upstreamStatus(us)).To(ContainSubstring("gloo-system:gateway: Pending"))
	})

})
