package cmd

import (
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/rebuy-de/aws-nuke/resources"
)

type Nuke struct {
	Parameters NukeParameters

	Config  *NukeConfig
	session *session.Session

	retry bool
	wait  bool

	queue    []resources.Resource
	waiting  []resources.Resource
	skipped  []resources.Resource
	failed   []resources.Resource
	finished []resources.Resource
}

func NewNuke(params NukeParameters) *Nuke {
	n := Nuke{
		Parameters: params,

		retry: true,
		wait:  true,

		queue:    []resources.Resource{},
		waiting:  []resources.Resource{},
		skipped:  []resources.Resource{},
		failed:   []resources.Resource{},
		finished: []resources.Resource{},
	}

	return &n
}

func (n *Nuke) StartSession() error {
	if n.Parameters.hasProfile() {
		s := session.New(&aws.Config{
			Region:      &n.Config.Region,
			Credentials: credentials.NewSharedCredentials("", n.Parameters.Profile),
		})

		if s == nil {
			return fmt.Errorf("Unable to create session with profile '%s'.", n.Parameters.Profile)
		}

		n.session = s
		return nil
	}

	if n.Parameters.hasKeys() {
		s := session.New(&aws.Config{
			Region: &n.Config.Region,
			Credentials: credentials.NewStaticCredentials(
				n.Parameters.AccessKeyID,
				n.Parameters.SecretAccessKey,
				"",
			),
		})

		if s == nil {
			return fmt.Errorf("Unable to create session with key ID '%s'.", n.Parameters.AccessKeyID)
		}

		n.session = s
		return nil
	}

	return fmt.Errorf("You have to specify a profile or credentials.")
}

func (n *Nuke) Run() error {
	var err error

	n.queue, err = n.Scan()
	if err != nil {
		return err
	}

	n.FilterQueue()
	n.HandleQueue()
	n.Wait()

	if n.retry {
		for len(n.failed) > 0 {
			fmt.Println()
			fmt.Printf("Retrying: %d finished, %d failed, %d skipped.",
				len(n.finished), len(n.failed), len(n.skipped))
			fmt.Println()
			fmt.Println()
			n.Retry()
		}
	}

	fmt.Println()
	fmt.Printf("Nuke complete: %d finished, %d failed, %d skipped.",
		len(n.finished), len(n.failed), len(n.skipped))
	fmt.Println()

	return err
}

func (n *Nuke) Scan() ([]resources.Resource, error) {
	listers := resources.GetListers(n.session)
	result := []resources.Resource{}

	for _, lister := range listers {
		resources, err := lister()
		if err != nil {
			return nil, err
		}

		result = append(result, resources...)
	}

	return result, nil
}

func (n *Nuke) FilterQueue() {
	temp := n.queue[:]
	n.queue = n.queue[0:0]

	for _, resource := range temp {
		checker, ok := resource.(resources.Filter)
		if !ok {
			n.queue = append(n.queue, resource)
			continue
		}

		err := checker.Filter()
		if err == nil {
			n.queue = append(n.queue, resource)
			continue
		}

		Log(resource, ReasonSkip, err.Error())
		n.skipped = append(n.skipped, resource)
	}
}

func (n *Nuke) Retry() {
	n.queue = n.failed[:]
	n.failed = n.failed[0:0]

	n.HandleQueue()
	n.Wait()
}

func (n *Nuke) HandleQueue() {
	temp := n.queue[:]
	n.queue = n.queue[0:0]

	for _, resource := range temp {
		if !n.Parameters.NoDryRun {
			n.skipped = append(n.skipped, resource)
			Log(resource, ReasonSuccess, "would remove")
			continue
		}

		err := resource.Remove()
		if err != nil {
			n.failed = append(n.failed, resource)
			Log(resource, ReasonError, err.Error())
			continue
		}

		n.waiting = append(n.waiting, resource)
		Log(resource, ReasonRemoveTriggered, "triggered remove")
	}
}

func (n *Nuke) Wait() {
	if !n.wait {
		n.finished = n.waiting
		n.waiting = []resources.Resource{}
		return
	}

	temp := n.waiting[:]
	n.waiting = n.waiting[0:0]

	var wg sync.WaitGroup
	for i, resource := range temp {
		waiter, ok := resource.(resources.Waiter)
		if !ok {
			n.finished = append(n.finished, resource)
			continue
		}
		wg.Add(1)
		Log(resource, ReasonWaitPending, "waiting")
		go func(i int, resource resources.Resource) {
			defer wg.Done()
			err := waiter.Wait()
			if err != nil {
				n.failed = append(n.failed, resource)
				Log(resource, ReasonError, err.Error())
				return
			}

			n.finished = append(n.finished, resource)
			Log(resource, ReasonSuccess, "removed")
		}(i, resource)
	}

	wg.Wait()
}