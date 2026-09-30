package writer

import (
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"icloud-reminders/internal/cloudkit"
	"icloud-reminders/internal/utils"
)

// CreateReminderOperation lets the service atomically create a reminder and
// publish its list order/section membership, rather than making two writes.
func CreateReminderOperation(title, listID, parentID, due string, priority int, notes string) (map[string]interface{}, string, error) {
	op, id, err := buildCreateOp(title, listID, parentID, due, priority, notes)
	if err != nil {
		return nil, "", err
	}
	id = cloudkit.NativeID("Reminder", id)
	op["record"].(map[string]interface{})["recordName"] = id
	fields := op["record"].(map[string]interface{})["fields"].(map[string]interface{})
	keys := []string{"titleDocument", "completed", "list"}
	if parentID != "" {
		keys = append(keys, "parentReminder")
	}
	if due != "" {
		keys = append(keys, "dueDate")
	}
	if priority != 0 {
		keys = append(keys, "priority")
	}
	if notes != "" {
		keys = append(keys, "notesDocument")
	}
	tokens, err := cloudkit.BumpResolutionTokens("", keys)
	if err != nil {
		return nil, "", err
	}
	fields["ResolutionTokenMap"] = map[string]interface{}{"value": tokens}
	return op, id, nil
}

// ApplyStructure requires a freshly read root and preserves untouched native
// resolution tokens. All record mutations use one atomic scoped request.
func (w *Writer) ApplyStructure(listID string, root map[string]interface{}, fields map[string]interface{}, documents map[string][]byte, additional []map[string]interface{}) error {
	scope, ok := w.Sync.Cache.Scopes[listID]
	if !ok {
		return fmt.Errorf("list scope unavailable")
	}
	client := w.CK.ForScope(scope)
	tag, _ := root["recordChangeTag"].(string)
	recordType, _ := root["recordType"].(string)
	if tag == "" || (recordType != "List" && recordType != "ReminderList") {
		return fmt.Errorf("list metadata unavailable")
	}
	rootFields, _ := root["fields"].(map[string]interface{})
	text, _ := cloudkit.FieldValue(rootFields, "ResolutionTokenMap").(string)
	keys := []string{}
	for _, pair := range [][2]string{{"ReminderIDs", "reminderIDsMergeableOrdering"}, {"ReminderIDsAsset", "reminderIDsMergeableOrdering"}, {"MembershipsOfRemindersInSectionsAsData", "membershipsOfRemindersInSectionsChecksum"}, {"SectionIDsOrderingAsData", "sectionIDsOrderingAsData"}} {
		if fields[pair[0]] != nil || documents[pair[0]] != nil {
			keys = append(keys, pair[1])
		}
	}
	encoded, err := cloudkit.BumpResolutionTokens(text, keys)
	if err != nil {
		return err
	}
	for _, op := range additional {
		if op["operationType"] != "update" {
			continue
		}
		record := op["record"].(map[string]interface{})
		changes := record["fields"].(map[string]interface{})
		if changes["ParentReminder"] != nil {
			id, _ := record["recordName"].(string)
			rd := w.Sync.Cache.Reminders[id]
			if rd == nil {
				return fmt.Errorf("reminder metadata unavailable")
			}
			tokens, err := cloudkit.BumpResolutionTokens(rd.ResolutionTokenMap, []string{"parentReminder"})
			if err != nil {
				return err
			}
			changes["ResolutionTokenMap"] = map[string]interface{}{"value": tokens}
		}
	}
	for field, data := range documents {
		asset, err := client.UploadStructureAsset(scope.OwnerRecordName, listID, root["recordType"].(string), field, data)
		if err != nil {
			return err
		}
		fields[field] = map[string]interface{}{"value": asset, "type": "ASSETID"}
		if field == "MembershipsOfRemindersInSectionsAsData" {
			hash := sha512.Sum512(data)
			fields["MembershipsOfRemindersInSectionsChecksum"] = map[string]interface{}{"value": strings.ToUpper(hex.EncodeToString(hash[:]))}
		}
	}
	if len(fields) > 0 {
		fields["ResolutionTokenMap"] = map[string]interface{}{"value": encoded}
		additional = append(additional, map[string]interface{}{"operationType": "update", "record": map[string]interface{}{"recordName": listID, "recordType": root["recordType"], "recordChangeTag": tag, "fields": fields}})
	}
	if len(additional) == 0 {
		return nil
	}
	result, err := client.ModifyRecords(scope.OwnerRecordName, additional)
	if err != nil {
		return err
	}
	if err := checkRecordErrors(result); err != nil {
		return err
	}
	// A full rebuild prevents local optimistic state from surviving incomplete
	// cache updates. The next request observes iCloud's authoritative structure.
	w.Sync.Cache.SchemaVersion = 0
	return w.Sync.Cache.Save()
}

func CreateSectionOperation(title, listID string) (map[string]interface{}, string) {
	id := "ListSection/" + utils.NewUUIDString()
	tokens, _ := cloudkit.BumpResolutionTokens("", []string{"displayName", "list"})
	return map[string]interface{}{"operationType": "create", "record": map[string]interface{}{
		"recordType": "ListSection", "recordName": id, "parent": map[string]interface{}{"recordName": listID},
		"fields": map[string]interface{}{"DisplayName": map[string]interface{}{"value": title}, "CreationDate": map[string]interface{}{"value": time.Now().UnixMilli()}, "List": map[string]interface{}{"value": map[string]interface{}{"recordName": listID, "action": "VALIDATE"}}, "ResolutionTokenMap": map[string]interface{}{"value": tokens}},
	}}, id
}
