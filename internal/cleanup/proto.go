package cleanup

import (
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"time"
)

func (r Report) Proto() *pb.CleanupReport {
	p := &pb.CleanupReport{Id: r.ID, Target: r.Target, StartedAt: r.StartedAt.Unix(), FinishedAt: r.FinishedAt.Unix(), DryRun: r.DryRun}
	for _, s := range r.Steps {
		p.Steps = append(p.Steps, &pb.CleanupStep{Name: s.Name, Candidates: int64(s.Candidates), Deleted: int64(s.Deleted), Skipped: int64(s.Skipped), Bytes: s.Bytes, Errors: s.Errors})
	}
	return p
}
func FromProto(p *pb.CleanupReport) Report {
	r := Report{ID: p.GetId(), Target: p.GetTarget(), StartedAt: time.Unix(p.GetStartedAt(), 0), FinishedAt: time.Unix(p.GetFinishedAt(), 0), DryRun: p.GetDryRun()}
	for _, s := range p.GetSteps() {
		r.Steps = append(r.Steps, Step{Name: s.Name, Candidates: int(s.Candidates), Deleted: int(s.Deleted), Skipped: int(s.Skipped), Bytes: s.Bytes, Errors: s.Errors})
	}
	return r
}
