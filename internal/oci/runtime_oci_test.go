package oci_test

import (
	"os"
	"path/filepath"
	"time"

	"github.com/cri-o/cri-o/internal/oci"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/rjeczalik/notify"
)

// The actual test suite
var _ = t.Describe("Oci", func() {
	t.Describe("WatchForFile", func() {
		var notifyFile string
		var done chan struct{}
		BeforeEach(func() {
			notifyFile = filepath.Join(t.MustTempDir("watch"), "file")
			done = make(chan struct{}, 1)
		})
		It("should catch file creation", func() {
			// Given
			ch, err := oci.WatchForFile(notifyFile, done, notify.InCreate, notify.InModify)
			Expect(err).To(BeNil())

			// When
			f, err := os.Create(notifyFile)
			Expect(err).To(BeNil())
			f.Close()

			<-ch
		})
		It("should not catch file create if doesn't exist", func() {
			// Given
			ch, err := oci.WatchForFile(notifyFile, done, notify.InCreate, notify.InModify)
			Expect(err).To(BeNil())

			// When
			f, err := os.Create(notifyFile + "-backup")
			Expect(err).To(BeNil())
			f.Close()
			checkChannelEmpty(ch)

			// Then
			f, err = os.Create(notifyFile)
			Expect(err).To(BeNil())
			f.Close()

			<-ch
		})
		It("should only catch file write", func() {
			// Given
			ch, err := oci.WatchForFile(notifyFile, done, notify.InModify)
			Expect(err).To(BeNil())

			// When
			f, err := os.Create(notifyFile)
			Expect(err).To(BeNil())
			defer f.Close()

			checkChannelEmpty(ch)

			_, err = f.Write([]byte("hello"))
			Expect(err).To(BeNil())

			<-ch
		})
		It("should give up after sending on done", func() {
			// Given
			ch, err := oci.WatchForFile(notifyFile, done, notify.InModify)
			Expect(err).To(BeNil())

			// When
			checkChannelEmpty(ch)
			done <- struct{}{}
			<-ch
		})
	})
})

func checkChannelEmpty(ch chan struct{}) {
	select {
	case <-ch:
		// We don't expect to get anything here
		Expect(true).To(Equal(false))
	case <-time.After(time.Second * 3):
	}
}
