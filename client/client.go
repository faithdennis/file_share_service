package client

import (
	"encoding/json"

	userlib "github.com/cs161-staff/project2-userlib"
	"github.com/google/uuid"

	// hex.EncodeToString(...) is useful for converting []byte to string

	// Useful for string manipulation
	"strings"

	// Useful for formatting strings (e.g. `fmt.Sprintf`).
	"fmt"

	// Useful for creating new error messages to return using errors.New("...")
	"errors"

	// Optional.
	_ "strconv"
)

// This is the type definition for the User struct.
// A Go struct is like a Python or Java class - it can have attributes
// (e.g. like the Username attribute) and methods (e.g. like the StoreFile method below).
const LENGTH = 16
const ENCRYPT = "encrypt"
const MAC = "mac"
const ACCESS = "access"

type User struct {
	Username  string
	RSAkey    userlib.PKEDecKey
	Sigkey    userlib.DSSignKey
	sourceKey []byte
}

type Access struct {
	MetaUUID            userlib.UUID
	MetaSourcekey       []byte // used to generate meta keys
	InvitationUUID      userlib.UUID
	InvitationSourcekey []byte // used to generate invitation keys
	InvitationList      userlib.UUID
	ListKey             []byte // used to generate invitation list keys
	IsOwner             bool
}

type InvitationList struct {
	Invitations map[userlib.UUID][]byte // invitation UUID to sourcekey
}

type InvitationMeta struct {
	InvitationUUID      userlib.UUID
	InvitationSourcekey []byte // used to generate invitation keys
}

type Invitation struct {
	MetaUUID      userlib.UUID
	MetaSourcekey []byte // used to generate meta keys
}

type Meta struct {
	Start         userlib.UUID
	Last          userlib.UUID
	FileSourcekey []byte // used as source key to generate file keys
}

type File struct {
	Contents []byte
	Next     userlib.UUID
}

func InitUser(username string, password string) (userdataptr *User, err error) {
	/* 
 	Creates a user for the service.
  	Requires a valid unused username. 
  	Returns a pointer to the generated user object and an error if applicable. 
	*/
	
	// error check: check if username is an empty string
	if username == "" {
		return nil, errors.New("username cannot be empty")
	}

	// generate UUID
	userUUID, err := GetUserUUID(username)
	if err != nil {
		return nil, err
	}

	// error check: check if username already exists
	_, ok := userlib.DatastoreGet(userUUID)
	if ok {
		return nil, errors.New("username already exists")
	}

	// generate source key
	sourceKey := GetSourceKey(username, password)

	// generate asynch and symmetric keys
	RSAPublicKey, RSAPrivateKey, DSSignKey, DSVerifyKey, err := GetAsynchKeys()
	if err != nil {
		return nil, errors.New("GetAsynchKeys error")
	}
	encryptKey, hmacKey, err := GetTwoHASHKDFKeys(sourceKey, ENCRYPT, MAC)
	if err != nil {
		return nil, errors.New("GetTwoHASHKDFKeys error")
	}

	// put public values into keystore
	userlib.KeystoreSet(username+" public key", RSAPublicKey)
	userlib.KeystoreSet(username+" signature key", DSVerifyKey)

	// create user struct
	userdata := User{
		Username:  username,
		RSAkey:    RSAPrivateKey,
		Sigkey:    DSSignKey,
		sourceKey: sourceKey,
	}

	// get encrypted msg and mac tag
	// userBytes, err := json.Marshal(userdata)
	msg, tag, err := EncryptThenMac(userdata, encryptKey, hmacKey)
	if err != nil {
		return nil, err
	}

	// generate value for datastore and store
	value, err := GenerateUUIDVal(msg, tag)
	if err != nil {
		return nil, errors.New("GenerateUUIDVal error")
	}
	userlib.DatastoreSet(userUUID, value)
	return &userdata, nil
}

func GetUser(username string, password string) (userdataptr *User, err error) {
	/* 
 	Autheticates user information and retrieves a pointer to the user object.
  	Requires information provided to match an existing user. 
  	Returns a pointer to the generated user object and an error if applicable. 
	*/
	
	// error check: empty username
	if username == "" {
		return nil, errors.New("username cannot be empty")
	}

	// generate UUID
	userUUID, err := GetUserUUID(username)
	if err != nil {
		return nil, errors.New("GetUserUUID error")
	}
	// error check: user doesn't exist
	encryptedUserdata, ok := userlib.DatastoreGet(userUUID)
	if !ok {
		return nil, errors.New("username does not exist")
	}

	// unpack data into msg and tag
	msg, tag, err := UnpackValue(encryptedUserdata)
	if err != nil {
		return nil, errors.New("failed to unpack user data")
	}

	// Generate the source key, encryption key, and HMAC key from the username and password
	sourceKey := GetSourceKey(username, password)
	encryptKey, hmacKey, err := GetTwoHASHKDFKeys(sourceKey, ENCRYPT, MAC)
	if err != nil {
		return nil, errors.New("failed to generate encryption and HMAC keys")
	}

	// HMAC Check
	err = CheckTag(msg, tag, hmacKey)
	if err != nil {
		return nil, errors.New("data integrity check failed: either wrong credentials or tampering")
	}

	//decrypt + unmarshall message
	decryptedMessage := userlib.SymDec(encryptKey, msg)
	var userdata User
	err = json.Unmarshal(decryptedMessage, &userdata)
	if err != nil {
		return nil, errors.New("failed to unmarshal user data")
	}

	userdata.sourceKey = sourceKey

	//username check
	if userdata.Username != username {
		return nil, errors.New("retrieved username does not match expected username")
	}
	return &userdata, nil
}

func (userdata *User) StoreFile(filename string, content []byte) (err error) {
	
	// Get accessUUID and keys
	accessUUID, err := GetAccessUUID(*userdata, filename)
	if err != nil {
		return errors.New("failed to get accessUUID")
	}
	accessSourceKey, err := GetAccessKey(userdata.sourceKey, filename)
	if err != nil {
		return errors.New("failed to get access sourcekey")
	}
	accessEncryptKey, accessHMACKey, err := GetTwoHASHKDFKeys(accessSourceKey, ENCRYPT, MAC)
	if err != nil {
		return errors.New("failed to get access encrypt and mac keys")
	}
	accessValue, ok := userlib.DatastoreGet(accessUUID)

	if ok {
		// Unpack, check tag, and decrypt
		accessMsg, accessTag, err := UnpackValue(accessValue)
		if err != nil {
			return errors.New("failed to unpack Access Struct")
		}
		err = CheckTag(accessMsg, accessTag, accessHMACKey)
		if err != nil {
			return errors.New("integrity check failed: Access Struct has been tampered with")
		}
		accessStruct, err := DecryptAccessMsg(accessMsg, accessEncryptKey)
		if err != nil {
			return errors.New("could not decrypt Access Struct")
		}

		// Get Meta UUID and keys
		metaUUID, metaSourceKey, err := GetMetaUUIDAndSourceKey(accessStruct)
		if err != nil {
			return errors.New("could not get Meta UUID and sourcekey")
		}
		metaEncryptKey, metaHMACKey, err := GetTwoHASHKDFKeys(metaSourceKey, ENCRYPT, MAC)
		if err != nil {
			return errors.New("could not get Meta encrypt and mac keys")
		}

		// Check if Meta exists, check tag, unpack, and decrypt
		metaValue, ok := userlib.DatastoreGet(metaUUID)
		if !ok {
			return errors.New("could not find Meta data in datastore")
		}
		metaMsg, metaTag, err := UnpackValue(metaValue)
		if err != nil {
			return errors.New("could not unpack Meta value")
		}
		err = CheckTag(metaMsg, metaTag, metaHMACKey)
		if err != nil {
			return errors.New("integrity check failed: Meta struct has been tampered with")
		}
		metaStruct, err := DecryptMetaMsg(metaMsg, metaEncryptKey)
		if err != nil {
			return errors.New("failed to decrypt Meta struct")
		}

		// Get start and end of files and keys for file
		startoffile := metaStruct.Start
		fileSourceKey := metaStruct.FileSourcekey
		// fileEncryptKey, fileHMACKey, err := GetTwoHASHKDFKeys(fileSourceKey, ENCRYPT, MAC)
		// Add tampering file check

		// Overwrite file and generate a new UUID for .Next of the file to update meta
		newNextUUID, err := AddFileToDatabase(startoffile, fileSourceKey, content)
		if err != nil {
			return err
		}
		metaStruct.Last = newNextUUID

		// Encrypt and mac meta and return it back to the datastore
		metaMsg, metaTag, err = EncryptThenMac(metaStruct, metaEncryptKey, metaHMACKey)
		if err != nil {
			return err
		}
		metaValue, err = GenerateUUIDVal(metaMsg, metaTag)
		if err != nil {
			return err
		}
		userlib.DatastoreSet(metaUUID, metaValue)

	} else {
		// Access does not exist. user must create a new file. Generate new file UUID and file keys
		fileUUID := uuid.New()
		fileSourceKey, err := GetRandomKey(userdata)
		if err != nil {
			return errors.New("failed to get file sourcekey")
		}

		// Add file to database
		nextFileUUID, err := AddFileToDatabase(fileUUID, fileSourceKey, content)
		if err != nil {
			return errors.New("failed to add file to datastore")
		}

		// Generate meta UUID and keys
		metaUUID := uuid.New()
		metaSourceKey, err := GetRandomKey(userdata)
		if err != nil {
			return errors.New("failed to get meta sourcekey")
		}
		metaEncryptKey, metaHMACKey, err := GetTwoHASHKDFKeys(metaSourceKey, ENCRYPT, MAC)
		if err != nil {
			return errors.New("failed to get file HDKF")
		}

		// Construct the metadata struct (UUIDs and keys), encrypt, mac, and store
		metaStruct := Meta{fileUUID, nextFileUUID, fileSourceKey}
		metaMsg, metaTag, err := EncryptThenMac(metaStruct, metaEncryptKey, metaHMACKey)
		if err != nil {
			return errors.New("failed to package data for entry into DataStore")
		}
		metaValue, err := GenerateUUIDVal(metaMsg, metaTag)
		if err != nil {
			return err
		}
		userlib.DatastoreSet(metaUUID, metaValue)

		// set list key
		userListKey, err := GetRandomKey(userdata)
		if err != nil {
			return err
		}

		invitationList := InvitationList{
			Invitations: make(map[uuid.UUID][]byte),
		}

		invitationListEncryptKey, invitationListHMACKey, err := GetTwoHASHKDFKeys(userListKey, ENCRYPT, MAC)
		if err != nil {
			errors.New("failed to generate encryption and HMAC keys for invite list Struct")
		}

		// Encrypt and mac meta and return it back to the datastore
		userListMsg, userListTag, err := EncryptThenMac(invitationList, invitationListEncryptKey, invitationListHMACKey)
		if err != nil {
			return err
		}

		inviteListValue, err := GenerateUUIDVal(userListMsg, userListTag)
		if err != nil {
			return err
		}
		inviteListUUID := uuid.New()
		userlib.DatastoreSet(inviteListUUID, inviteListValue)

		// Create the owner struct
		ownerStruct := Access{
			MetaUUID:       metaUUID,
			MetaSourcekey:  metaSourceKey,
			InvitationList: inviteListUUID,
			ListKey:        userListKey,
			IsOwner:        true,
		}

		if !ownerStruct.IsOwner {
			return errors.New("error making struct")
		}

		// access encrypt then mac
		ownerMsg, ownerTag, err := EncryptThenMacAccess(ownerStruct, accessEncryptKey, accessHMACKey)
		if err != nil {
			return err
		}

		// package the values
		ownerValue, err := GenerateUUIDVal(ownerMsg, ownerTag)
		if err != nil {
			return err
		}
		userlib.DatastoreSet(accessUUID, ownerValue)
	}
	return nil
}

func (userdata *User) LoadFile(filename string) (content []byte, err error) {
	// Get the access UUID and check if it exists
	accessUUID, err := GetAccessUUID(*userdata, filename)
	if err != nil {
		return nil, errors.New("failed to get accessUUID")
	}
	accessValue, ok := userlib.DatastoreGet(accessUUID)
	if !ok {
		return nil, errors.New("file does not exist in user namespace")
	}

	// Generate the source key, encryption key, and HMAC key
	accessSourceKey, err := GetAccessKey(userdata.sourceKey, filename)
	if err != nil {
		return nil, errors.New("failed to get access sourcekey")
	}
	accessEncryptKey, accessHMACKey, err := GetTwoHASHKDFKeys(accessSourceKey, ENCRYPT, MAC)
	if err != nil {
		return nil, errors.New("failed to generate encryption and HMAC keys for Access Struct")
	}

	// Unpack, check tag, and decrypt
	accessMsg, accessTag, err := UnpackValue(accessValue)
	if err != nil {
		return nil, errors.New("failed to unpack Access Struct")
	}
	err = CheckTag(accessMsg, accessTag, accessHMACKey)
	if err != nil {
		return nil, errors.New("integrity check failed: Access Struct has been tampered with")
	}
	accessStruct, err := DecryptAccessMsg(accessMsg, accessEncryptKey)
	if err != nil {
		return nil, errors.New("could not decrypt access message")
	}

	// Get meta UUID and keys
	metaUUID, metaSourceKey, err := GetMetaUUIDAndSourceKey(accessStruct)
	if err != nil {
		return nil, errors.New("could not get Meta UUID and soucekey") // this will error if they do not have access
	}
	metaEncryptKey, metaHMACKey, err := GetTwoHASHKDFKeys(metaSourceKey, ENCRYPT, MAC)
	if err != nil {
		return nil, errors.New("could not get Meta encrypt and mac keys")
	}

	// Check if meta exists, check tag, unpack, and decrypt
	metaValue, ok := userlib.DatastoreGet(metaUUID)
	if !ok {
		return nil, errors.New("could not find Meta data in datastore")
	}
	metaMsg, metaTag, err := UnpackValue(metaValue)
	if err != nil {
		return nil, errors.New("could not unpack Meta value")
	}
	err = CheckTag(metaMsg, metaTag, metaHMACKey)
	if err != nil {
		return nil, errors.New("integrity check failed: Meta struct has been tampered with")
	}
	metaStruct, err := DecryptMetaMsg(metaMsg, metaEncryptKey)
	if err != nil {
		return nil, errors.New("failed to decrypt Meta struct")
	}

	// Get start and end of files and keys for file
	startoffile, endoffile := metaStruct.Start, metaStruct.Last
	fileSourceKey := metaStruct.FileSourcekey
	fileEncryptKey, fileHMACKey, err := GetTwoHASHKDFKeys(fileSourceKey, ENCRYPT, MAC)
	if err != nil {
		return nil, errors.New("failed to get keys for File")
	}

	// Declare variable for storing file contents and iterate through file components
	var fullContent []byte
	var fileContent []byte

	currentUUID := startoffile
	for currentUUID != endoffile {
		// Fetch the file data block from the datastore
		fileValue, ok := userlib.DatastoreGet(currentUUID)
		if !ok {
			return nil, errors.New("File data block not found")
		}

		// Unpack, check tag, and decrypt
		fileMsg, fileTag, err := UnpackValue(fileValue)
		if err != nil {
			return nil, errors.New("file could not be unpacked")
		}
		err = CheckTag(fileMsg, fileTag, fileHMACKey)
		if err != nil {
			return nil, errors.New("integrity check failed: File has unauthorized modifications")
		}
		fileStruct, err := DecryptFileMsg(fileMsg, fileEncryptKey)
		if err != nil {
			return nil, errors.New("File could not be decrypted")
		}
		fileContent = fileStruct.Contents

		// Append this file to entire message
		fullContent = append(fullContent, fileContent...)

		currentUUID = fileStruct.Next
	}

	return fullContent, nil
}

func (userdata *User) AppendToFile(filename string, content []byte) error {
	// Get the access UUID and check if it exists
	accessUUID, err := GetAccessUUID(*userdata, filename)
	if err != nil {
		return errors.New("failed to get access UUID sourcekey")
	}
	accessValue, ok := userlib.DatastoreGet(accessUUID)
	if !ok {
		return errors.New("File does not exist in user namespace")
	}

	// Generate the source key, encryption key, and HMAC key
	accessSourceKey, err := GetAccessKey(userdata.sourceKey, filename)
	if err != nil {
		return errors.New("failed to get access sourcekey")
	}
	accessEncryptKey, accessHMACKey, err := GetTwoHASHKDFKeys(accessSourceKey, ENCRYPT, MAC)
	if err != nil {
		return errors.New("failed to generate encryption and HMAC keys for Access Struct")
	}

	// Unpack, check tag, and decrypt
	accessMsg, accessTag, err := UnpackValue(accessValue)
	if err != nil {
		return errors.New("failed to unpack Access Struct")
	}
	err = CheckTag(accessMsg, accessTag, accessHMACKey)
	if err != nil {
		return errors.New("integrity check failed: Access Struct has been tampered with")
	}
	accessStruct, err := DecryptAccessMsg(accessMsg, accessEncryptKey)
	if err != nil {
		return errors.New("could not decrypt Access Struct")
	}

	// Get meta UUID and keys
	metaUUID, metaSourceKey, err := GetMetaUUIDAndSourceKey(accessStruct)
	if err != nil {
		return errors.New("could not get Meta UUID and soucekey") // this will error if they do not have accesss
	}
	metaEncryptKey, metaHMACKey, err := GetTwoHASHKDFKeys(metaSourceKey, ENCRYPT, MAC)
	if err != nil {
		return errors.New("could not get Meta encrypt and mac keys")
	}

	// Check if meta exists, check tag, unpack, and decrypt
	metaValue, ok := userlib.DatastoreGet(metaUUID)
	if !ok {
		return errors.New("could not find Meta data in datastore")
	}
	metaMsg, metaTag, err := UnpackValue(metaValue)
	if err != nil {
		return errors.New("could not unpack Meta value")
	}
	err = CheckTag(metaMsg, metaTag, metaHMACKey)
	if err != nil {
		return errors.New("integrity check failed: Meta struct has been tampered with")
	}
	metaStruct, err := DecryptMetaMsg(metaMsg, metaEncryptKey)
	if err != nil {
		return errors.New("failed to decrypt Meta struct")
	}

	fileSourceKey := metaStruct.FileSourcekey
	lastUUID := metaStruct.Last

	// FILE INFORMATION
	nextFileUUID, err := AddFileToDatabase(lastUUID, fileSourceKey, content)
	if err != nil {
		return err
	}
	metaStruct.Last = nextFileUUID

	// Encrypt and Mac updated meta
	metaMsg, metaTag, err = EncryptThenMac(metaStruct, metaEncryptKey, metaHMACKey)
	if err != nil {
		return err
	}

	// generate UUID value
	metaValue, err = GenerateUUIDVal(metaMsg, metaTag)
	if err != nil {
		return err
	}
	userlib.DatastoreSet(metaUUID, metaValue)
	return nil
}

func (userdata *User) CreateInvitation(filename string, recipientUsername string) (
	invitationPtr uuid.UUID, err error) {
	// check if user exits by seeing if their key exists in public keystore
	_, ok := userlib.KeystoreGet(recipientUsername + " public key")
	if !ok {
		return uuid.Nil, errors.New("recipient user does not exist in the system")
	}

	// errors if recipient is itself
	if recipientUsername == userdata.Username {
		return uuid.Nil, errors.New("user cannot send invitation to themselves")

	}

	// Get the access UUID, check if it exists, then get keys
	accessUUID, err1 := GetAccessUUID(*userdata, filename)
	if err1 != nil {
		return uuid.Nil, err
	}
	accessValue, ok := userlib.DatastoreGet(accessUUID)
	if !ok {
		return uuid.Nil, errors.New("file does not exist in user namespace")
	}
	accessSourceKey, err := GetAccessKey(userdata.sourceKey, filename)
	if err != nil {
		return uuid.Nil, errors.New("failed to get access sourcekey")
	}
	accessEncryptKey, accessHMACKey, err := GetTwoHASHKDFKeys(accessSourceKey, ENCRYPT, MAC)
	if err != nil {
		return uuid.Nil, errors.New("failed to generate encryption and HMAC keys for Access Struct")
	}

	// Unpack, check tag, and decrypt access struct
	accessMsg, accessTag, err := UnpackValue(accessValue)
	if err != nil {
		return uuid.Nil, errors.New("failed to unpack Access Struct")
	}
	err = CheckTag(accessMsg, accessTag, accessHMACKey)
	if err != nil {
		return uuid.Nil, errors.New("integrity check failed: Access Struct has been tampered with")
	}
	accessStruct, err := DecryptAccessMsg(accessMsg, accessEncryptKey)
	if err != nil {
		return uuid.Nil, errors.New("could not decrypt Access Struct")
	}

	// Get meta UUID and keys
	metaUUID, metaSourceKey, err := GetMetaUUIDAndSourceKey(accessStruct)
	if err != nil {
		return uuid.Nil, errors.New("could not get Meta UUID and soucekey")
	}

	// Generate a new shared key for the invitation
	invitationSourceKey, err := GetRandomKey(userdata)
	if err != nil {
		return userlib.UUID{}, errors.New("failed to generate source key")
	}

	// get keys
	inviteEncryptKey, inviteHMACKey, err := GetTwoHASHKDFKeys(invitationSourceKey, ENCRYPT, MAC)
	if err != nil {
		return uuid.Nil, errors.New("failed to generate keys for invite")
	}

	// create invitation
	invitation := Invitation{
		MetaUUID:      metaUUID,
		MetaSourcekey: metaSourceKey,
	}

	// Encrypt the invite and create an HMAC tag
	inviteMsg, inviteTag, err := EncryptThenMac(invitation, inviteEncryptKey, inviteHMACKey)
	if err != nil {
		return uuid.Nil, errors.New("failed to package data for entry into DataStore")
	}

	// Store the encrypted invite and the HMAC tag in the datastore
	invitationValue, err := GenerateUUIDVal(inviteMsg, inviteTag)
	if err != nil {
		return uuid.Nil, err
	}

	// MAKE THIS DETERMINIMISTIC
	invitationUUID, err := GetInvitationUUID(userdata, recipientUsername, filename)
	if err != nil {
		return uuid.Nil, err
	}
	userlib.DatastoreSet(invitationUUID, invitationValue)

	// create meta uuid
	//TODO MAKE THIS RANDOM
	invitationMetaUUID := uuid.New()

	// create meta invitation
	invitationMeta := InvitationMeta{
		InvitationUUID:      invitationUUID,
		InvitationSourcekey: invitationSourceKey,
	}

	// encrypt, sign, and store invitation Meta
	invitationMetaMsg, invitationMetaSig, err := EncryptThenSign(invitationMeta, recipientUsername, userdata.Sigkey)
	if err != nil {
		return uuid.Nil, err
	}
	invitationMetaValue, err := GenerateUUIDVal(invitationMetaMsg, invitationMetaSig)
	if err != nil {
		return uuid.Nil, err
	}

	userlib.DatastoreSet(invitationMetaUUID, invitationMetaValue)

	// also add invitationUUID, invitationSourceKey to invite list of owner
	if accessStruct.IsOwner {
		// get invitation list
		inviteListUUID := accessStruct.InvitationList
		inviteListKey := accessStruct.ListKey
		inviteListData, ok := userlib.DatastoreGet(inviteListUUID)
		if !ok {
			return uuid.Nil, errors.New("invalid or missing inviteListData UUID")
		}

		// Unpack the invitation data
		inviteListMsg, inviteListTag, err := UnpackValue(inviteListData)
		if err != nil {
			return uuid.Nil, errors.New("failed to unpack invitationList data")
		}

		inviteListEncryptKey, inviteListHMACKey, err := GetTwoHASHKDFKeys(inviteListKey, ENCRYPT, MAC)
		if err != nil {
			return uuid.Nil, err
		}

		// check tag
		err = CheckTag(inviteListMsg, inviteListTag, inviteListHMACKey)
		if err != nil {
			return uuid.Nil, errors.New("integrity check failed: invite struct has been tampered with")
		}
		// decrypt invitation list using invitation list key

		invitationListValue, err := DecryptInvitationListMsg(inviteListMsg, inviteListEncryptKey)

		// add value to the map
		invitationListValue.Invitations[invitationUUID] = invitationSourceKey

		// re-encrypt + hmac
		invitationListEncryptKey, invitationListHMACKey, err := GetTwoHASHKDFKeys(inviteListKey, ENCRYPT, MAC)
		if err != nil {
			errors.New("failed to generate encryption and HMAC keys for invite list Struct")
		}

		// Encrypt and mac meta and return it back to the datastore
		inviteListMsg, inviteListTag, err = EncryptThenMac(invitationListValue, invitationListEncryptKey, invitationListHMACKey)
		if err != nil {
			return uuid.Nil, err
		}

		inviteListValue, err := GenerateUUIDVal(inviteListMsg, inviteListTag)
		if err != nil {
			return uuid.Nil, err
		}
		userlib.DatastoreSet(inviteListUUID, inviteListValue)
	}

	// add invitation
	return invitationMetaUUID, nil
}

func (userdata *User) AcceptInvitation(senderUsername string, invitationPtr uuid.UUID, filename string) error {
	// Check if the recipient already has a file with the chosen filename
	accessUUID, err := GetAccessUUID(*userdata, filename)
	if err != nil {
		return errors.New("could not get access uuid")
	}
	_, ok := userlib.DatastoreGet(accessUUID)
	if ok {
		return errors.New("recipient already has a file with the chosen filename")
	}

	// Get invitation metadata from Datastore
	invitationMetaValue, ok := userlib.DatastoreGet(invitationPtr)
	if !ok {
		return errors.New("no invitation meta")
	}

	// Unpack the invitation data, verify sender's signature, and decrypt the invitation
	invitationMetaMsg, invitationMetaSig, err := UnpackValue(invitationMetaValue)
	if err != nil {
		return errors.New("failed to unpack invitation data")
	}
	err = CheckSignature(invitationMetaMsg, invitationMetaSig, senderUsername)
	if err != nil {
		return errors.New("failed to verify invitation signature")
	}
	invitationMetaStruct, err := DecryptAsynchMsg(invitationMetaMsg, userdata.RSAkey)
	if err != nil {
		return errors.New("failed to decrypt invitation")
	}

	// Get invitation UUID and invitation keys
	invitationUUID := invitationMetaStruct.InvitationUUID
	invitationSourceKey := invitationMetaStruct.InvitationSourcekey

	// Get the invitation from the datastore to check the tag
	inviteData, ok := userlib.DatastoreGet(invitationUUID)
	if !ok {
		return errors.New("invalid or missing invitation UUID")
	}
	// Unpack the invitation data
	inviteMsg, inviteTag, err := UnpackValue(inviteData)
	if err != nil {
		return errors.New("failed to unpack invitation data")
	}
	// generate keys
	_, inviteHMACKey, err := GetTwoHASHKDFKeys(invitationSourceKey, ENCRYPT, MAC)
	if err != nil {
		return err
	}
	// check tag
	err = CheckTag(inviteMsg, inviteTag, inviteHMACKey)
	if err != nil {
		return errors.New("integrity check failed: invite struct has been tampered with")
	}

	// create an access struct and get the keys
	accessStruct := Access{
		InvitationUUID:      invitationUUID,
		InvitationSourcekey: invitationSourceKey,
	}
	accessSourceKey, err := GetAccessKey(userdata.sourceKey, filename)
	if err != nil {
		return errors.New("access source key cannot be generated")
	}
	accessEncKey, accessHMACKey, err := GetTwoHASHKDFKeys(accessSourceKey, ENCRYPT, MAC)
	if err != nil {
		return err
	}

	// Encrypt the access, HMAC, and store
	accessMsg, accessTag, err := EncryptThenMac(accessStruct, accessEncKey, accessHMACKey)
	if err != nil {
		return errors.New("failed to package data for entry into DataStore")
	}
	accessData, err := GenerateUUIDVal(accessMsg, accessTag)
	if err != nil {
		return err
	}
	userlib.DatastoreSet(accessUUID, accessData)
	return nil
}

func (userdata *User) RevokeAccess(filename string, recipientUsername string) error {
	// Get the access UUID and check if it exists
	accessUUID, err := GetAccessUUID(*userdata, filename)
	if err != nil {
		return errors.New("failed to get access sourcekey")
	}
	accessValue, ok := userlib.DatastoreGet(accessUUID)
	if !ok {
		return errors.New("file does not exist in user namespace")
	}

	// Generate the source key, encryption key, and HMAC key
	accessSourceKey, err := GetAccessKey(userdata.sourceKey, filename)
	if err != nil {
		return errors.New("failed to get access sourcekey")
	}
	accessEncryptKey, accessHMACKey, err := GetTwoHASHKDFKeys(accessSourceKey, ENCRYPT, MAC)
	if err != nil {
		return errors.New("failed to generate encryption and HMAC keys for Access Struct")
	}

	// Unpack, check tag, and decrypt
	accessMsg, accessTag, err := UnpackValue(accessValue)
	if err != nil {
		return errors.New("failed to unpack Access Struct")
	}
	err = CheckTag(accessMsg, accessTag, accessHMACKey)
	if err != nil {
		return errors.New("integrity check failed: Access Struct has been tampered with")
	}
	accessStruct, err := DecryptAccessMsg(accessMsg, accessEncryptKey)
	if err != nil {
		return errors.New("could not decrypt Access Struct")
	}

	if !accessStruct.IsOwner {
		return errors.New("only the owner can revoke access")
	}

	// Get meta UUID and keys
	metaUUID, metaSourceKey, err := GetMetaUUIDAndSourceKey(accessStruct)
	if err != nil {
		return errors.New("could not get Meta UUID and soucekey")
	}
	_, metaHMACKey, err := GetTwoHASHKDFKeys(metaSourceKey, ENCRYPT, MAC)
	if err != nil {
		return errors.New("could not get Meta encrypt and mac keys")
	}

	// Check if meta exists, check tag, unpack, and decrypt
	metaValue, ok := userlib.DatastoreGet(metaUUID)
	if !ok {
		return errors.New("could not find Meta data in datastore")
	}
	metaMsg, metaTag, err := UnpackValue(metaValue)
	if err != nil {
		return errors.New("could not unpack Meta value")
	}
	err = CheckTag(metaMsg, metaTag, metaHMACKey)
	if err != nil {
		return errors.New("integrity check failed: Meta struct has been tampered with")
	}

	// Decrypt file contents
	content, err := userdata.LoadFile(filename)
	if err != nil {
		return errors.New("failed to load file contents")
	}

	// Generate new keys and encrypt file contents at a new UUID
	fileUUID := uuid.New()
	fileSourceKey, err := GetRandomKey(userdata)
	if err != nil {
		return errors.New("failed to get new sourcekey for file")
	}
	nextFileUUID, err := AddFileToDatabase(fileUUID, fileSourceKey, content)
	if err != nil {
		return errors.New("failed to add to database")
	}

	// Generate a new UUID for meta, meta struct, and meta keys
	metaStruct := Meta{fileUUID, nextFileUUID, fileSourceKey}
	metaSourceKey, err = GetRandomKey(userdata)
	if err != nil {
		return errors.New("failed to get new sourcekey for meta")
	}
	metaEncryptKey, metaHMACKey, err := GetTwoHASHKDFKeys(metaSourceKey, ENCRYPT, MAC)
	if err != nil {
		return err
	}

	// Encrypt, mac, and store new meta
	metaMsg, metaTag, err = EncryptThenMac(metaStruct, metaEncryptKey, metaHMACKey)
	if err != nil {
		return err
	}
	metaValue, err = GenerateUUIDVal(metaMsg, metaTag)
	if err != nil {
		return err
	}
	userlib.DatastoreSet(metaUUID, metaValue)

	// Get invitationList struct location and keys
	invitationListUUID := accessStruct.InvitationList
	invitationListKey := accessStruct.ListKey
	invitationListEncryptKey, invitationListHMACKey, err := GetTwoHASHKDFKeys(invitationListKey, ENCRYPT, MAC)
	if err != nil {
		return err
	}

	// Get value, unpack, check tag, and decrypt
	invitationListValue, ok := userlib.DatastoreGet(invitationListUUID)
	if !ok {
		return errors.New("failed to get invitation list from Datastore")
	}
	invitationListMsg, invitationListTag, err := UnpackValue(invitationListValue)
	if err != nil {
		return errors.New("failed to unpack invitation list")
	}
	err = CheckTag(invitationListMsg, invitationListTag, invitationListHMACKey)
	if err != nil {
		return errors.New("integrity check failed: detected unauthorized modifications")
	}
	invitationListStruct, err := DecryptInvitationListMsg(invitationListMsg, invitationListEncryptKey)
	if err != nil {
		return errors.New("failed to decrypt invitation list struct")
	}

	// Get recipient invitation UUID
	recipientInvitationUUID, err := GetInvitationUUID(userdata, recipientUsername, filename)
	if err != nil {
		return errors.New("error getting invitation uuid for recipient")
	}

	// Check if the target user is in the invitation list
	_, exists := invitationListStruct.Invitations[recipientInvitationUUID]
	if !exists {
		return errors.New("filename was not shared with recipientUsername")
	}

	// Delete recipient from invitationsList
	invitations := invitationListStruct.Invitations
	delete(invitations, recipientInvitationUUID)

	// Iterate over invitations list getting keys, decrypting, updating, and encrypting
	for invitationUUID, invitationSourceKey := range invitations {
		// Get keys
		invitationEncryptKey, invitationHMACKey, err := GetTwoHASHKDFKeys(invitationSourceKey, ENCRYPT, MAC)
		if err != nil {
			return err
		}

		// Update invitation information
		invitationStruct := Invitation{metaUUID, metaSourceKey}
		invitationMsg, invitationTag, err := EncryptThenMac(invitationStruct, invitationEncryptKey, invitationHMACKey)
		if err != nil {
			return errors.New("failed to encrypt and mac invitation struct")
		}
		invitationValue, err := GenerateUUIDVal(invitationMsg, invitationTag)
		if err != nil {
			return errors.New("failed to get UUID value for invitation")
		}
		userlib.DatastoreSet(invitationUUID, invitationValue)
	}

	// Update invitation list, encrypt it, and add it back to datastore
	invitationListStruct.Invitations = invitations

	invitationListMsg, invitationListTag, err = EncryptThenMac(invitationListStruct, invitationListEncryptKey, invitationListHMACKey)
	if err != nil {
		return errors.New("failed to encrypt then mac updated invitation list struct")
	}
	invitationListValue, err = GenerateUUIDVal(invitationListMsg, invitationListTag)
	if err != nil {
		return errors.New("failed to generate then mac updated invitation list UUID value")
	}
	userlib.DatastoreSet(invitationListUUID, invitationListValue)

	// Update owner struct, encrypt it, and add it back to the datastore
	accessStruct.MetaSourcekey = metaSourceKey
	accessMsg, accessTag, err = EncryptThenMac(accessStruct, accessEncryptKey, accessHMACKey)
	if err != nil {
		return errors.New("failed to encrypt and mac new owner struct")
	}
	updatedOwnerValue, err := GenerateUUIDVal(accessMsg, accessTag)
	if err != nil {
		return errors.New("failed to get UUID value for owner")
	}
	userlib.DatastoreSet(accessUUID, updatedOwnerValue)

	return nil
}

// Helper Functions

// assumes password has sufficient entropy to create non-bruteforceable UUID and sourcekey
// only use the username to determine where the stuff is at,
func GetUserUUID(user string) (UUID userlib.UUID, err error) {
	// generate uuid
	userbytes := []byte(user)
	salt1 := []byte("UUID")
	UUID, err = uuid.FromBytes(userlib.Argon2Key(userbytes, salt1, LENGTH))

	// check for error
	if err != nil {
		return uuid.UUID{}, errors.New(strings.ToTitle("conversion to UUID failed"))
	}
	return
}

func GetSourceKey(user, password string) (sourcekey []byte) {
	passwordbytes := []byte(password)
	sourcekey = userlib.Argon2Key(passwordbytes, []byte(user), LENGTH)
	return
}

func GetAsynchKeys() (pk userlib.PKEEncKey, sk userlib.PKEDecKey, signpriv userlib.DSSignKey, signpub userlib.DSVerifyKey, err error) {
	// generate asymmetric encryption keys
	pk, sk, err = userlib.PKEKeyGen()

	// generate asymmetric signature keys
	signpriv, signpub, err1 := userlib.DSKeyGen()

	// check for errors
	if err != nil {
		return pk, sk, signpriv, signpub, errors.New(strings.ToTitle("rSA KeyGen failed"))
	}
	if err1 != nil {
		return pk, sk, signpriv, signpub, errors.New(strings.ToTitle("signature KeyGen failed"))
	}
	return
}

// given secure source key this should produce fast secure keys
func GetTwoHASHKDFKeys(sourcekey []byte, purpose1, purpose2 string) (key1, key2 []byte, err error) {
	// generate keys and check errors
	key, err := userlib.HashKDF(sourcekey, []byte(purpose1))
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("key creation failed"))
	}
	key1 = key[:16]
	key, err = userlib.HashKDF(sourcekey, []byte(purpose2))
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("key creation failed"))
	}
	key2 = key[:LENGTH]
	return
}

func GetAccessUUID(user User, filename string) (UUID userlib.UUID, err error) {
	// hash username and check error
	accessbytes := []byte(filename)
	accesshash, err := userlib.HashKDF(user.sourceKey, accessbytes)
	if err != nil {
		return uuid.UUID{}, errors.New(strings.ToTitle("hashing failed"))
	}

	// convert to byte and check error
	UUID, err = uuid.FromBytes(accesshash[:LENGTH])
	if err != nil {
		return uuid.UUID{}, errors.New(strings.ToTitle("Conversion to UUID failed"))
	}
	return
}

func GetInvitationUUID(owner *User, sharee, filename string) (UUID userlib.UUID, err error) {
	// hash username and check error
	invitebytes := []byte(owner.Username + filename + sharee)
	invitehash, err := userlib.HashKDF(owner.sourceKey, invitebytes)
	if err != nil {
		return uuid.UUID{}, errors.New(strings.ToTitle("Hashing failed"))
	}

	// convert to byte and check error
	UUID, err = uuid.FromBytes(invitehash[:LENGTH])
	if err != nil {
		return uuid.UUID{}, errors.New(strings.ToTitle("file not found"))
	}
	return
}

func GenerateUUIDVal(msg, tag []byte) (value []byte, err error) {
	// create map
	Map := map[string][]byte{
		"Msg": msg,
		"Tag": tag,
	}

	// generate byte array
	value, err = json.Marshal(Map)
	if err != nil {
		return nil, errors.New(strings.ToTitle("file not found"))
	}
	return
}

func UnpackValue(value []byte) (msg, tag []byte, err error) {
	// unmarshall datastore value
	unpackedData := make(map[string][]byte)
	err = json.Unmarshal(value, &unpackedData)

	// check for error unmarshalling and return
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("unmarshal failed"))
	}
	msg, tag = unpackedData["Msg"], unpackedData["Tag"]
	return
}

func EncryptThenMac(txt interface{}, key1, key2 []byte) (msg, tag []byte, err error) {
	// convert text to bytes and check for error
	plaintext, err := json.Marshal(txt)
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("marshal failed"))
	}

	// encrypt and mac
	rndbytes := userlib.RandomBytes(LENGTH)
	msg = userlib.SymEnc(key1, rndbytes, plaintext)
	tag, err = userlib.HMACEval(key2, msg)

	// check for error and return
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("hmac failed"))
	}
	return
}

func EncryptThenMacAccess(txt Access, key1, key2 []byte) (msg, tag []byte, err error) {
	// convert text to bytes and check for error
	plaintext, err := json.Marshal(txt)
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("marshal failed"))
	}

	// encrypt and mac
	rndbytes := userlib.RandomBytes(LENGTH)
	msg = userlib.SymEnc(key1, rndbytes, plaintext)
	tag, err = userlib.HMACEval(key2, msg)

	// check for error and return
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("hmac failed"))
	}
	return
}

func EncryptThenSign(txt InvitationMeta, user string, sk userlib.DSSignKey) (msg, sig []byte, err error) {
	// convert to byte array, check for error
	plaintext, err := json.Marshal(txt)
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("marshal failed"))
	}

	// encrypt using user public key, check for error
	pubkey, ok := userlib.KeystoreGet(user + " public key")
	if !ok {
		return nil, nil, errors.New(strings.ToTitle("keystoreGet failed"))
	}
	ciphertext, err := userlib.PKEEnc(pubkey, plaintext)
	if err != nil {
		return nil, nil, errors.New(strings.ToTitle("encryption failed"))
	}

	// sign, check for error, and return
	sig, err = userlib.DSSign(sk, ciphertext)
	return ciphertext, sig, err
}

func CheckTag(msg, tag, key2 []byte) (err error) {
	// compute tag and check error
	computedTag, err := userlib.HMACEval(key2, msg)
	if err != nil {
		return err
	}

	if userlib.HMACEqual(tag, computedTag) {
		return
	}
	return errors.New("integrity check failed")
}

func CheckSignature(msg, sig []byte, user string) (err error) {
	// get verification key, check error
	sk, ok := userlib.KeystoreGet(user + " signature key")
	if !ok {
		return errors.New("could not get sign key")
	}

	// verify signature
	err = userlib.DSVerify(sk, msg, sig)
	return
}

func DecryptFileMsg(msg, key1 []byte) (data File, err error) {
	// decrypt msg
	plaintext := userlib.SymDec(key1, msg)

	// unmarshal data to get original struct
	err = json.Unmarshal(plaintext, &data)
	return
}

func DecryptAccessMsg(msg, key1 []byte) (data Access, err error) {
	// decrypt msg
	plaintext := userlib.SymDec(key1, msg)

	// unmarshal data to get original struct
	err = json.Unmarshal(plaintext, &data)
	return
}

func DecryptMetaMsg(msg, key1 []byte) (data Meta, err error) {
	// decrypt msg
	plaintext := userlib.SymDec(key1, msg)

	// unmarshal data to get original struct
	err = json.Unmarshal(plaintext, &data)
	return
}

func DecryptInvitationMsg(msg, key1 []byte) (data Invitation, err error) {
	// decrypt msg
	plaintext := userlib.SymDec(key1, msg)

	// unmarshal data to get original struct
	err = json.Unmarshal(plaintext, &data)
	return
}

func DecryptInvitationListMsg(msg, key1 []byte) (data InvitationList, err error) {
	// decrypt msg
	plaintext := userlib.SymDec(key1, msg)

	// unmarshal data to get original struct
	err = json.Unmarshal(plaintext, &data)
	return
}

func DecryptInvitationMetaMsg(msg, key1 []byte) (data InvitationMeta, err error) {
	// decrypt msg
	plaintext := userlib.SymDec(key1, msg)

	// unmarshal data to get original struct
	err = json.Unmarshal(plaintext, &data)
	return
}

func DecryptAsynchMsg(msg []byte, pk userlib.PKEDecKey) (data InvitationMeta, err error) {
	// decrypt msg
	plaintext, err := userlib.PKEDec(pk, msg)
	if err != nil {
		return InvitationMeta{}, errors.New(strings.ToTitle("decryption failed"))
	}

	// unmarshal data to get original struct and check for error
	err = json.Unmarshal(plaintext, &data)
	if err != nil {
		return InvitationMeta{}, errors.New(strings.ToTitle("unmarshalling failed"))
	}
	return
}

func GetRandomKey(user *User) (key []byte, err error) {
	// generate new random key
	sourcekey, salt := user.sourceKey, userlib.RandomBytes(LENGTH)
	hashedkey, err := userlib.HashKDF(sourcekey, salt)
	key = hashedkey[:LENGTH]
	// check for error
	if err != nil {
		return nil, errors.New(strings.ToTitle("file not found"))
	}
	return
}

func GetAccessStruct(invitation userlib.UUID, sourcekey []byte) (access interface{}) {
	access = Access{InvitationUUID: invitation, InvitationSourcekey: sourcekey, IsOwner: false}
	return
}

func GetAccessKey(sourcekey []byte, filename string) (key []byte, err error) {
	hashedkey, err := userlib.HashKDF(sourcekey, []byte(filename))
	key = hashedkey[:LENGTH]
	if err != nil {
		return nil, errors.New(strings.ToTitle("key creation failed"))
	}
	return
}

func AddFileToDatabase(fileUUID userlib.UUID, fileSourceKey, content []byte) (nextFileUUID userlib.UUID, err error) {
	// generate UUID for next
	nextFileUUID = uuid.New()

	// generate keys
	fileEncryptKey, fileHMACKey, err := GetTwoHASHKDFKeys(fileSourceKey, ENCRYPT, MAC)
	if err != nil {
		return uuid.Nil, errors.New("failed to get keys")
	}

	// generate file struct
	file := File{
		Contents: content,
		Next:     nextFileUUID,
	}

	// encrypt file struct
	encryptedBytes, tag, err := EncryptThenMac(file, fileEncryptKey, fileHMACKey)
	if err != nil {
		return uuid.Nil, errors.New("failed to EncryptThenMac")
	}

	// create value and add to Datastore
	value, err := GenerateUUIDVal(encryptedBytes, tag)
	if err != nil {
		return uuid.Nil, errors.New("failed to package data for entry into DataStore")
	}
	userlib.DatastoreSet(fileUUID, value)
	return
}

func GetMetaUUIDAndSourceKey(accessStruct Access) (metaUUID userlib.UUID, metaSourceKey []byte, err error) {
	// check if user obtained access through invitation
	userOwnsFile := accessStruct.IsOwner
	if !userOwnsFile {
		// get UUID and keys for invitation
		invitationUUID := accessStruct.InvitationUUID
		invitationSourceKey := accessStruct.InvitationSourcekey
		invitationEncryptKey, invitationHMACKey, err := GetTwoHASHKDFKeys(invitationSourceKey, ENCRYPT, MAC)
		if err != nil {
			return uuid.New(), nil, errors.New("could not get keys")
		}

		// check if invitation exists, check tag, unpack, and decrypt
		invitationValue, ok := userlib.DatastoreGet(invitationUUID)
		if !ok {
			return uuid.Nil, nil, errors.New("invitation does not exist")
		}
		invitationMsg, invitationTag, err := UnpackValue(invitationValue)
		if err != nil {
			return uuid.Nil, nil, errors.New("could not unpack invitation value")
		}
		err = CheckTag(invitationMsg, invitationTag, invitationHMACKey)
		if err != nil {
			return uuid.Nil, nil, errors.New("integrity check failed: Invitation struct has unauthorized modifications")
		}
		invitationStruct, err := DecryptInvitationMsg(invitationMsg, invitationEncryptKey)
		if err != nil {
			return uuid.Nil, nil, errors.New("could not decrypt Invitation Struct")
		}

		// get UUID and sourcekey of meta file
		metaUUID = invitationStruct.MetaUUID
		metaSourceKey = invitationStruct.MetaSourcekey
	} else {
		metaUUID = accessStruct.MetaUUID
		metaSourceKey = accessStruct.MetaSourcekey
	}
	return
}

func UnpackCheckTagAndDecryptFile(fileUUID userlib.UUID, fileEncryptKey, fileHMACKey []byte) (fileStruct File, err error) {
	fileValue, ok := userlib.DatastoreGet(fileUUID)
	if !ok {
		return File{}, errors.New("file value was not found in DataStore")
	}
	fileMsg, fileTag, err := UnpackValue(fileValue)
	if err != nil {
		return File{}, errors.New("file could not be unpacked")
	}
	err = CheckTag(fileMsg, fileTag, fileHMACKey)
	if err != nil {
		return File{}, errors.New("integrity check failed: File has unauthorized modifications")
	}
	fileStruct, err = DecryptFileMsg(fileMsg, fileEncryptKey)
	if err != nil {
		return File{}, errors.New("file could not be decrypted")
	}
	return
}

func GetInvitationSourceKey(sourceKey []byte, user, filename string) (invitationSourceKey []byte, err error) {
	hashedinvitationSourceKey, err := userlib.HashKDF(sourceKey, []byte(user+ACCESS+filename))
	invitationSourceKey = hashedinvitationSourceKey[:LENGTH]
	if err != nil {
		return nil, errors.New("failed to get invitation li")
	}
	return
}
