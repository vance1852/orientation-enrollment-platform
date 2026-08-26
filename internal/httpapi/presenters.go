package httpapi

import (
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

// meetingView renders one weekly block.
type meetingView struct {
	Weekday     string `json:"weekday"`
	StartMinute int    `json:"start_minute"`
	EndMinute   int    `json:"end_minute"`
	Room        string `json:"room"`
	Label       string `json:"label"`
}

// sectionView renders a teaching section. The version is exposed so a client can
// detect that the seat counters it displays are stale.
type sectionView struct {
	ID             int64         `json:"id"`
	TermID         int64         `json:"term_id"`
	Code           string        `json:"code"`
	CourseCode     string        `json:"course_code"`
	Credits        int           `json:"credits"`
	Status         string        `json:"status"`
	Capacity       int           `json:"capacity"`
	SeatsTaken     int           `json:"seats_taken"`
	SeatsAvailable int           `json:"seats_available"`
	WaitlistLimit  int           `json:"waitlist_limit"`
	WaitlistLength int           `json:"waitlist_length"`
	Instructor     string        `json:"instructor"`
	Version        int64         `json:"version"`
	Meetings       []meetingView `json:"meetings"`
}

// termView renders a term with its enrollment deadlines.
type termView struct {
	ID                 int64     `json:"id"`
	Code               string    `json:"code"`
	Name               string    `json:"name"`
	EnrollmentOpensAt  time.Time `json:"enrollment_opens_at"`
	EnrollmentClosesAt time.Time `json:"enrollment_closes_at"`
	AddDropClosesAt    time.Time `json:"add_drop_closes_at"`
	CreditLimit        int       `json:"credit_limit"`
	Archived           bool      `json:"archived"`
}

// registrationView renders orientation paperwork.
type registrationView struct {
	ID             int64      `json:"id"`
	StudentID      int64      `json:"student_id"`
	TermID         int64      `json:"term_id"`
	Status         string     `json:"status"`
	ProgramCode    string     `json:"program_code"`
	AdvisorEmail   string     `json:"advisor_email"`
	DormPreference string     `json:"dorm_preference"`
	SubmittedAt    *time.Time `json:"submitted_at"`
	DecidedAt      *time.Time `json:"decided_at"`
	DecisionNote   string     `json:"decision_note"`
	Version        int64      `json:"version"`
}

// enrollmentView renders a seat claim.
type enrollmentView struct {
	ID            int64      `json:"id"`
	StudentID     int64      `json:"student_id"`
	TermID        int64      `json:"term_id"`
	SectionID     int64      `json:"section_id"`
	CourseCode    string     `json:"course_code"`
	Credits       int        `json:"credits"`
	Status        string     `json:"status"`
	WaitlistRank  int        `json:"waitlist_rank"`
	RequestedAt   time.Time  `json:"requested_at"`
	DecidedAt     *time.Time `json:"decided_at"`
	ReleasedAt    *time.Time `json:"released_at"`
	ReleaseReason string     `json:"release_reason"`
	Version       int64      `json:"version"`
}

// auditEventView renders one trail entry.
type auditEventView struct {
	ID          int64     `json:"id"`
	ActorUserID *int64    `json:"actor_user_id"`
	ActorRole   string    `json:"actor_role"`
	Action      string    `json:"action"`
	ObjectType  string    `json:"object_type"`
	ObjectID    string    `json:"object_id"`
	Result      string    `json:"result"`
	RequestID   string    `json:"request_id"`
	Detail      string    `json:"detail"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// pageMeta is the paging envelope shared by every list endpoint.
type pageMeta struct {
	Total      int `json:"total"`
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	TotalPages int `json:"total_pages"`
}

type listEnvelope[T any] struct {
	Items []T      `json:"items"`
	Meta  pageMeta `json:"meta"`
}

func newMeta[T any](result domain.PageResult[T]) pageMeta {
	return pageMeta{
		Total:      result.Total,
		Page:       result.Page,
		PageSize:   result.PageSize,
		TotalPages: result.TotalPages,
	}
}

func toMeetingViews(meetings []domain.Meeting) []meetingView {
	views := make([]meetingView, 0, len(meetings))
	for _, meeting := range meetings {
		views = append(views, meetingView{
			Weekday:     meeting.Weekday.String(),
			StartMinute: meeting.StartMinute,
			EndMinute:   meeting.EndMinute,
			Room:        meeting.Room,
			Label:       meeting.Label(),
		})
	}
	return views
}

func toSectionView(section domain.Section) sectionView {
	return sectionView{
		ID:             section.ID,
		TermID:         section.TermID,
		Code:           section.Code,
		CourseCode:     section.CourseCode,
		Credits:        section.CourseCredits,
		Status:         string(section.Status),
		Capacity:       section.Capacity,
		SeatsTaken:     section.SeatsTaken,
		SeatsAvailable: section.SeatsAvailable(),
		WaitlistLimit:  section.WaitlistLimit,
		WaitlistLength: section.WaitlistLength,
		Instructor:     section.Instructor,
		Version:        section.Version,
		Meetings:       toMeetingViews(section.Meetings),
	}
}

func toTermView(term domain.Term) termView {
	return termView{
		ID:                 term.ID,
		Code:               term.Code,
		Name:               term.Name,
		EnrollmentOpensAt:  term.EnrollmentOpensAt,
		EnrollmentClosesAt: term.EnrollmentClosesAt,
		AddDropClosesAt:    term.AddDropClosesAt,
		CreditLimit:        term.CreditLimit,
		Archived:           term.Archived,
	}
}

func toRegistrationView(registration domain.Registration) registrationView {
	return registrationView{
		ID:             registration.ID,
		StudentID:      registration.StudentID,
		TermID:         registration.TermID,
		Status:         string(registration.Status),
		ProgramCode:    registration.ProgramCode,
		AdvisorEmail:   registration.AdvisorEmail,
		DormPreference: registration.DormPreference,
		SubmittedAt:    registration.SubmittedAt,
		DecidedAt:      registration.DecidedAt,
		DecisionNote:   registration.DecisionNote,
		Version:        registration.Version,
	}
}

func toEnrollmentView(enrollment domain.Enrollment) enrollmentView {
	return enrollmentView{
		ID:            enrollment.ID,
		StudentID:     enrollment.StudentID,
		TermID:        enrollment.TermID,
		SectionID:     enrollment.SectionID,
		CourseCode:    enrollment.CourseCode,
		Credits:       enrollment.Credits,
		Status:        string(enrollment.Status),
		WaitlistRank:  enrollment.WaitlistRank,
		RequestedAt:   enrollment.RequestedAt,
		DecidedAt:     enrollment.DecidedAt,
		ReleasedAt:    enrollment.ReleasedAt,
		ReleaseReason: enrollment.ReleaseReason,
		Version:       enrollment.Version,
	}
}

func toAuditEventView(event domain.AuditEvent) auditEventView {
	return auditEventView{
		ID:          event.ID,
		ActorUserID: event.ActorUserID,
		ActorRole:   event.ActorRole,
		Action:      string(event.Action),
		ObjectType:  event.ObjectType,
		ObjectID:    event.ObjectID,
		Result:      string(event.Result),
		RequestID:   event.RequestID,
		Detail:      event.Detail,
		OccurredAt:  event.OccurredAt,
	}
}
