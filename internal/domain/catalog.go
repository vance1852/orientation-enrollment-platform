package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// BusinessLocationName is the single timezone all orientation deadlines are
// expressed in. Enrollment windows are campus local, not client local.
const BusinessLocationName = "Asia/Shanghai"

// Term is one academic term with an explicit enrollment window and an
// add/drop window that closes later than the initial enrollment window.
type Term struct {
	ID                 int64
	Code               string
	Name               string
	EnrollmentOpensAt  time.Time
	EnrollmentClosesAt time.Time
	AddDropClosesAt    time.Time
	CreditLimit        int
	Archived           bool
}

// Validate checks the internal consistency of a term definition.
func (t Term) Validate() error {
	if strings.TrimSpace(t.Code) == "" {
		return NewFieldError("term.code", "must not be empty")
	}
	if t.CreditLimit <= 0 {
		return NewFieldError("term.credit_limit", "must be positive")
	}
	if !t.EnrollmentOpensAt.Before(t.EnrollmentClosesAt) {
		return NewFieldError("term.enrollment_closes_at", "must be after enrollment_opens_at")
	}
	if t.AddDropClosesAt.Before(t.EnrollmentClosesAt) {
		return NewFieldError("term.add_drop_closes_at", "must not be before enrollment_closes_at")
	}
	return nil
}

// EnrollmentOpen reports whether new seats may be claimed at the given instant.
func (t Term) EnrollmentOpen(now time.Time) bool {
	if t.Archived {
		return false
	}
	return !now.Before(t.EnrollmentOpensAt) && now.Before(t.EnrollmentClosesAt)
}

// DropAllowed reports whether a student may still release a seat themselves.
func (t Term) DropAllowed(now time.Time) bool {
	if t.Archived {
		return false
	}
	return !now.Before(t.EnrollmentOpensAt) && now.Before(t.AddDropClosesAt)
}

// Course is a catalogue entry. Credits feed the per-term credit limit and
// Prerequisites gate the enrollment eligibility check.
type Course struct {
	ID            int64
	Code          string
	Title         string
	Credits       int
	Department    string
	Prerequisites []string
	Retired       bool
}

// Validate checks catalogue invariants that are independent of persistence.
func (c Course) Validate() error {
	if strings.TrimSpace(c.Code) == "" {
		return NewFieldError("course.code", "must not be empty")
	}
	if c.Credits <= 0 || c.Credits > 12 {
		return NewFieldError("course.credits", "must be between 1 and 12")
	}
	return nil
}

// Weekday mirrors time.Weekday but is persisted as a small integer so meeting
// rows stay comparable inside SQL.
type Weekday int

// Weekday values used by the meeting schedule.
const (
	Sunday Weekday = iota
	Monday
	Tuesday
	Wednesday
	Thursday
	Friday
	Saturday
)

var weekdayNames = map[Weekday]string{
	Sunday:    "sunday",
	Monday:    "monday",
	Tuesday:   "tuesday",
	Wednesday: "wednesday",
	Thursday:  "thursday",
	Friday:    "friday",
	Saturday:  "saturday",
}

// String renders the campus facing weekday name.
func (w Weekday) String() string {
	if name, ok := weekdayNames[w]; ok {
		return name
	}
	return fmt.Sprintf("weekday(%d)", int(w))
}

// Valid reports whether the weekday is inside the supported range.
func (w Weekday) Valid() bool { return w >= Sunday && w <= Saturday }

// Meeting is one weekly time block of a section, stored as minutes from
// midnight in the business timezone.
type Meeting struct {
	ID          int64
	SectionID   int64
	Weekday     Weekday
	StartMinute int
	EndMinute   int
	Room        string
}

// Validate checks that the block is a well formed, non-empty interval.
func (m Meeting) Validate() error {
	if !m.Weekday.Valid() {
		return NewFieldError("meeting.weekday", "must be between 0 and 6")
	}
	if m.StartMinute < 0 || m.StartMinute >= 24*60 {
		return NewFieldError("meeting.start_minute", "must be within a single day")
	}
	if m.EndMinute <= m.StartMinute || m.EndMinute > 24*60 {
		return NewFieldError("meeting.end_minute", "must be after start_minute and within a single day")
	}
	return nil
}

// Overlaps reports whether two weekly blocks collide. Blocks that merely touch
// at a boundary minute do not collide, which lets back-to-back classes coexist.
func (m Meeting) Overlaps(other Meeting) bool {
	if m.Weekday != other.Weekday {
		return false
	}
	return m.StartMinute < other.EndMinute && other.StartMinute < m.EndMinute
}

// Label renders a human readable block, used by conflict messages.
func (m Meeting) Label() string {
	return fmt.Sprintf("%s %02d:%02d-%02d:%02d", m.Weekday,
		m.StartMinute/60, m.StartMinute%60, m.EndMinute/60, m.EndMinute%60)
}

// SectionStatus is the lifecycle of a teaching section.
type SectionStatus string

// Section lifecycle values.
const (
	SectionDraft     SectionStatus = "draft"
	SectionOpen      SectionStatus = "open"
	SectionClosed    SectionStatus = "closed"
	SectionCancelled SectionStatus = "cancelled"
)

// Section is one offering of a course inside a term. Version powers the
// optimistic seat allocation used by concurrent enrollment requests.
type Section struct {
	ID             int64
	TermID         int64
	CourseID       int64
	CourseCode     string
	CourseCredits  int
	Code           string
	Status         SectionStatus
	Capacity       int
	SeatsTaken     int
	WaitlistLimit  int
	WaitlistLength int
	Instructor     string
	Version        int64
	Meetings       []Meeting
	UpdatedAt      time.Time
}

// SeatsAvailable reports how many seats can still be claimed.
func (s Section) SeatsAvailable() int {
	free := s.Capacity - s.SeatsTaken
	if free < 0 {
		return 0
	}
	return free
}

// AcceptsEnrollment reports whether the section itself allows a new seat claim.
func (s Section) AcceptsEnrollment() bool { return s.Status == SectionOpen }

// CloneMeetings returns a defensive copy so repository callers can never mutate
// cached or shared backing arrays.
func (s Section) CloneMeetings() []Meeting {
	if len(s.Meetings) == 0 {
		return nil
	}
	out := make([]Meeting, len(s.Meetings))
	copy(out, s.Meetings)
	return out
}

// SortMeetings orders blocks by weekday then start minute so conflict messages
// and API responses are stable.
func SortMeetings(meetings []Meeting) {
	sort.Slice(meetings, func(i, j int) bool {
		if meetings[i].Weekday != meetings[j].Weekday {
			return meetings[i].Weekday < meetings[j].Weekday
		}
		if meetings[i].StartMinute != meetings[j].StartMinute {
			return meetings[i].StartMinute < meetings[j].StartMinute
		}
		return meetings[i].EndMinute < meetings[j].EndMinute
	})
}

// FindScheduleConflict returns the first colliding pair between a candidate
// section and the blocks the student already holds. The second return value is
// false when the candidate fits.
func FindScheduleConflict(candidate []Meeting, held []Meeting) (Meeting, Meeting, bool) {
	for _, c := range candidate {
		for _, h := range held {
			if c.Overlaps(h) {
				return c, h, true
			}
		}
	}
	return Meeting{}, Meeting{}, false
}
