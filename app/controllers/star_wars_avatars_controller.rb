class StarWarsAvatarsController < ApplicationController

    def show
      response.headers['Content-Type'] = 'image/svg+xml'
      render inline: Rails.application.assets.find_asset('star-wars-avatars/darth.svg').pathname.read
    end

end
