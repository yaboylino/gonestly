package sql

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type sharedKeyUser struct {
	ID      int64         `db:"id"`
	Profile *sharedKeyBio `rel:"id"`
}

type sharedKeyBio struct {
	ID int64 `db:"id"`
}

type sharedKeyUserHas struct {
	ID      int64         `db:"id"`
	Profile *sharedKeyBio `rel:"id,has"`
}

type keylessLine struct {
	OrderID int64 `db:"order_id"`
}

type keylessOrder struct {
	ID    int64         `db:"id"`
	Lines []keylessLine `rel:"order_id"`
}

type otherTarget struct {
	ID int64 `db:"id"`
}

func TestRelationDirectionAmbiguous(t *testing.T) {
	m, err := modelOf(reflect.TypeFor[sharedKeyUser]())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.resolveRelations(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want an ambiguity error, got %v", err)
	}
	m, err = modelOf(reflect.TypeFor[sharedKeyUserHas]())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.resolveRelations(); err != nil {
		t.Fatal(err)
	}
	if m.byRel["Profile"].kind != hasOne {
		t.Fatalf("want has one, got %s", m.byRel["Profile"].kind)
	}
}

func TestCreateNestedWithoutPrimaryKeyIsAnError(t *testing.T) {
	m, err := modelOf(reflect.TypeFor[keylessOrder]())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := m.relation("Lines")
	if err != nil {
		t.Fatal(err)
	}
	order := keylessOrder{Lines: []keylessLine{{}}}
	err = createNested(t.Context(), store{model: rel.targetModel}, reflect.ValueOf(&order.Lines[0]).Elem(), &nestedState{seen: map[uintptr]bool{}})
	if !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("want ErrInvalidModel, got %v", err)
	}
}

func TestIncludeRejectsWrongTarget(t *testing.T) {
	m, err := modelOf(reflect.TypeFor[sharedKeyUserHas]())
	if err != nil {
		t.Fatal(err)
	}
	err = checkNodes(m, []includeNode{{name: "Profile", target: reflect.TypeFor[otherTarget]()}})
	if !errors.Is(err, ErrInvalidModel) {
		t.Fatalf("want ErrInvalidModel, got %v", err)
	}
}
