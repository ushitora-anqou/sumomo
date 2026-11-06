# Creates an OAuth application and user access token for the e2e test.
# Run via: kubectl exec -i deploy/<web> -- bash -c 'cat > /tmp/mk.rb && bin/rails runner /tmp/mk.rb'
email = ENV.fetch('E2E_USER_EMAIL')
app = Doorkeeper::Application.create!(name: 'e2e', redirect_uri: 'urn:ietf:wg:oauth:2.0:oob', scopes: 'read write')
user = User.find_by(email: email)
token = Doorkeeper::AccessToken.create!(application: app, resource_owner_id: user.id, scopes: 'read write')
puts "TOKEN=#{token.token}"
